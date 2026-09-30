// Package client: HTTP access to BoursoBank's three planes with the audited
// transport block, dual-domain cookies, bearer bootstrap and error taxonomy.
package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

// var (not const) so tests can point them at an httptest server — the only
// test seam: overridable package vars.
var (
	apiBase   = "https://api.boursobank.com/services/api/v1.7"
	dashboard = "https://clients.boursobank.com/"
)

var (
	reBearer = regexp.MustCompile(`"DEFAULT_API_BEARER":"([^"]+)"`)
	reHash   = regexp.MustCompile(`"USER_HASH":"([^"]+)"`)
)

// Client holds the merged dual-domain cookie header + scraped bearer/userHash.
type Client struct {
	hc     *http.Client
	cookie string // merged .boursobank.com + .boursorama.com jars (memory only)
	ua     string
	// cookieSource loads the cookie jar on the first cookie-plane request
	// (bearer-only commands never touch the Chrome cookie store).
	cookieSource func(context.Context) (string, error)
	// allowRefresh lets the transport send the one non-GET request
	// (POST session/auth/refresh). Off by default: the CLI only reads.
	allowRefresh bool
	// recover is called when the bank reports an expired session in the
	// middle of a command (see resilientGet) — at most once per process, so
	// a session that cannot be fixed never loops on the Chrome keychain.
	// nil = no recovery.
	recover   func(context.Context) error
	recovered bool
	Bearer    string
	UserHash  string
}

// reUserHash: the bank's user hash goes into every bearer-plane path.
var reUserHash = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validUserHash(h string) error {
	if !reUserHash.MatchString(h) {
		return fmt.Errorf("USER_HASH invalide (%d caractères, format inattendu) — refusé dans les URL", len(h))
	}
	return nil
}

// ErrBearerRejected means the bank refused the bearer (session expired or
// replaced). Any other Probe error is a network/bank problem, not a reason
// to touch the Chrome cookie store.
var ErrBearerRejected = errors.New("bearer refusé par la banque")

// New builds the audited transport: proxy-from-env, TLS>=1.2, hard timeout,
// HTTP/2 disabled (some Bourso frontends hang on H2 from Go), NO cookie jar
// (we send the merged Cookie header manually — the dual-domain requirement),
// transparent gzip (Go default). Every request, redirects included, goes
// through the egress guard (see egress.go).
func New(mergedCookie, userAgent string) *Client {
	if userAgent == "" {
		userAgent = defaultUA
	}
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{}, // disable HTTP/2
		ForceAttemptHTTP2: false,
	}
	c := &Client{
		cookie: mergedCookie,
		ua:     userAgent,
	}
	c.hc = &http.Client{Timeout: 20 * time.Second, Transport: &guard{next: tr, c: c}}
	// We carry no cookie jar (deliberate, dual-domain). Go DROPS the
	// Cookie header on a cross-host redirect, so the cookie-plane export
	// chain (clients.boursobank.com → api.boursobank.com/files/
	// download.phtml) lands unauthenticated → 401. Re-attach the session
	// cookie — but ONLY on a cookie-plane request (the first hop carried
	// the cookie) and ONLY to an allow-listed HTTPS host. The guard then
	// refuses any hop outside the allow-list before it is sent.
	c.hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("trop de redirections")
		}
		if err := egress.check(req.URL); err != nil {
			return fmt.Errorf("redirection refusée depuis %s : %w — la banque renvoie ailleurs (page de connexion, maintenance ?) : ouvrir BoursoBank dans Chrome, se reconnecter si besoin, puis relancer", via[len(via)-1].URL.Host, err)
		}
		req.Header.Set("user-agent", c.ua)
		if via[0].Header.Get("cookie") != "" {
			//nolint:gosec // G119: only on cookie-plane requests, only to allow-listed HTTPS hosts (egress.check above)
			req.Header.Set("cookie", c.cookie)
		} else {
			req.Header.Del("cookie")
		}
		return nil
	}
	return c
}

// SetCookieSource installs the lazy cookie loader (see cookieSource).
func (c *Client) SetCookieSource(f func(context.Context) (string, error)) { c.cookieSource = f }

// SetCookie replaces the cookie jar ("" = load again from the source on
// the next cookie-plane request).
func (c *Client) SetCookie(merged string) { c.cookie = merged }

// AllowRefresh enables the POST session/auth/refresh request.
func (c *Client) AllowRefresh(on bool) { c.allowRefresh = on }

// SetRecover installs the mid-command session recovery (see recover).
func (c *Client) SetRecover(f func(context.Context) error) { c.recover = f }

// ensureCookie loads the cookie jar if it is not loaded yet.
func (c *Client) ensureCookie(ctx context.Context) error {
	if c.cookie != "" || c.cookieSource == nil {
		return nil
	}
	ck, err := c.cookieSource(ctx)
	if err != nil {
		return err
	}
	c.cookie = ck
	return nil
}

// Bootstrap GETs the dashboard with the merged cookie and scrapes the 24h
// DEFAULT_API_BEARER + USER_HASH. A 302→/connexion or missing bearer ⇒ the
// Chrome session is dead (re-login in Chrome).
func (c *Client) Bootstrap(ctx context.Context) error {
	body, status, _, err := c.do(ctx, http.MethodGet, dashboard, "text/html", false)
	if err != nil {
		return err
	}
	mb, mh := reBearer.FindSubmatch(body), reHash.FindSubmatch(body)
	if mb == nil || mh == nil {
		// Fallback (api-reference: the `brsxds_<hex32>` cookie value IS a
		// fresh API JWT, exp≈iat+24h, userHash inside). Decode it from the
		// cookie we already hold — removes the dependency on a successful
		// dashboard scrape (the exact failure here). Honest limit: if the
		// server session is hard-dead the JWT is still rejected later (10006)
		// and the resilientGet refresh/relogin path handles that — this only
		// kills the "dashboard didn't yield the bearer" failure class.
		if jwt, uh, ok := c.bearerFromCookie(); ok {
			c.Bearer, c.UserHash = jwt, uh
			return nil
		}
		return fmt.Errorf("pas de DEFAULT_API_BEARER dans le dashboard (HTTP %d) ni de cookie brsxds_ valide : la session Chrome BoursoBank est morte — se reconnecter dans Chrome, puis réessayer", status)
	}
	if err := validUserHash(string(mh[1])); err != nil {
		return err
	}
	c.Bearer, c.UserHash = string(mb[1]), string(mh[1])
	return nil
}

// bearerFromCookie extracts a still-valid API JWT from a `brsxds_<hex32>`
// cookie in the merged jar (its value is the JWT). Returns ok only when the
// token is a well-formed, non-expired JWT carrying a userHash — otherwise the
// caller keeps its existing dead-session error (no behaviour regression).
func (c *Client) bearerFromCookie() (jwt, userHash string, ok bool) {
	for _, p := range strings.Split(c.cookie, ";") {
		p = strings.TrimSpace(p)
		i := strings.IndexByte(p, '=')
		if i <= 0 || !strings.HasPrefix(p[:i], "brsxds_") {
			continue
		}
		tok := p[i+1:]
		if v, err := url.QueryUnescape(tok); err == nil {
			tok = v
		}
		parts := strings.Split(tok, ".")
		if len(parts) != 3 || !strings.HasPrefix(tok, "eyJ") {
			continue
		}
		raw, err := b64urlDecode(parts[1])
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		exp, _ := m["exp"].(float64)
		if exp == 0 || time.Now().Add(60*time.Second).After(time.Unix(int64(exp), 0)) {
			continue // expired or about to — don't substitute a dead token
		}
		uh := claimUserHash(m)
		if validUserHash(uh) != nil {
			continue
		}
		return tok, uh, true
	}
	return "", "", false
}

func b64urlDecode(s string) ([]byte, error) {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(s)
}

// claimUserHash finds the userHash claim (key name not contractually fixed —
// match defensively; absent ⇒ caller declines the fallback).
func claimUserHash(m map[string]any) string {
	for k, v := range m {
		if s, isStr := v.(string); isStr && s != "" && strings.EqualFold(strings.ReplaceAll(k, "_", ""), "userhash") {
			return s
		}
	}
	return ""
}

// BearerExp returns the JWT exp as a UTC time, or zero if unparseable.
func (c *Client) BearerExp() time.Time {
	parts := strings.Split(c.Bearer, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	raw, err := b64urlDecode(parts[1])
	if err != nil {
		return time.Time{}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return time.Time{}
	}
	exp, _ := m["exp"].(float64)
	if exp == 0 {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0).UTC()
}

// Refresh renews the server-side session WITHOUT re-scraping the dashboard
// Clean reconnect path: POST _public_/session/auth/refresh,
// body {}, cookie plane, no bearer. 200 = renewed. It is the only non-GET
// request: the guard refuses it unless AllowRefresh(true).
func (c *Client) Refresh(ctx context.Context) error {
	if !c.allowRefresh {
		return errRefreshDisabled
	}
	if err := c.ensureCookie(ctx); err != nil {
		return err
	}
	u := apiBase + refreshPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", c.ua)
	req.Header.Set("origin", "https://clients.boursobank.com")
	req.Header.Set("referer", "https://clients.boursobank.com/")
	req.Header.Set("cookie", c.cookie)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("session/auth/refresh → HTTP %d (session non renouvelable ; se reconnecter dans Chrome)", resp.StatusCode)
	}
	return nil
}

// Probe checks whether the current bearer is still accepted by the server.
// A lightweight GET on a known-cheap endpoint; returns nil if 200,
// ErrBearerRejected on an auth refusal (401 that is not an edge throttle,
// or 10006), and a plain error on anything else (network, throttle, 5xx).
// No retry, no re-auth.
func (c *Client) Probe(ctx context.Context) error {
	if err := validUserHash(c.UserHash); err != nil {
		return fmt.Errorf("%w (%w)", ErrBearerRejected, err)
	}
	u := fmt.Sprintf("%s/_user_/_%s_/customer/timeline/unreadcount?_host=clients.boursobank.com", apiBase, c.UserHash)
	b, st, _, err := c.do(ctx, http.MethodGet, u, "application/json", true)
	if err != nil {
		return err
	}
	switch {
	case st == 200:
		return nil
	case isThrottled(st, b):
		return fmt.Errorf("probe → HTTP %d (throttle en bordure, la session n’est pas en cause — réessayer plus tard)", st)
	case st == 401 || st == 403 || isBankSessionExpired(st, b):
		return fmt.Errorf("%w (probe → HTTP %d)", ErrBearerRejected, st)
	default:
		return fmt.Errorf("probe → HTTP %d", st)
	}
}

// API calls a Bearer-plane endpoint: GET api.boursobank.com/.../_user_/_<hash>_/<resource>.
// Pass the resource WITHOUT the userHash segment (added here). NO cookie sent.
// Resilient: throttle → bounded backoff (no re-auth); bank 401/10006 → one
// recover() + one retry — except on trading/, where 10006 means "session
// not elevated to bourse" (see cli.getJSON), which a new bearer cannot fix.
func (c *Client) API(ctx context.Context, resource string) ([]byte, int, error) {
	if err := validUserHash(c.UserHash); err != nil {
		return nil, 0, err
	}
	u := fmt.Sprintf("%s/_user_/_%s_/%s?_host=clients.boursobank.com", apiBase, c.UserHash, resource)
	if strings.HasPrefix(resource, "trading/") {
		return c.getWith(ctx, u, "application/json", true, false)
	}
	return c.resilientGet(ctx, u, "application/json", true)
}

// PublicAPI calls a public Bearer-plane endpoint (_public_/ prefix, no userHash).
func (c *Client) PublicAPI(ctx context.Context, resource string) ([]byte, int, error) {
	u := fmt.Sprintf("%s/%s", apiBase, resource)
	return c.resilientGet(ctx, u, "application/json", true)
}

// Cookie calls a cookie-plane URL with the merged dual-domain cookie.
// Resilient like API. NOT for one-shot URLs (download.phtml token) — use
// CookieOnce there so a throttled retry can't burn the token.
func (c *Client) Cookie(ctx context.Context, fullURL string) ([]byte, int, error) {
	return c.resilientGet(ctx, fullURL, "text/html, */*", false)
}

// CookieOnce is a single cookie-plane GET with no retry/backoff — for
// non-idempotent / one-shot URLs.
func (c *Client) CookieOnce(ctx context.Context, fullURL string) ([]byte, int, error) {
	b, st, _, err := c.do(ctx, http.MethodGet, fullURL, "text/html, */*", false)
	return b, st, err
}

// resilientGet wraps an idempotent GET with two SEPARATE
// recovery loops:
//   - throttle (Varnish "401 V"/"Not Authorized", or 503): exponential
//     backoff + jitter, NO re-auth (the session is fine) — max 3 tries.
//   - bank session 401/10006 (bearer plane): one recover() (set by the
//     caller: a new bearer from the live Chrome session, or Refresh() when
//     the owner allowed it) then one retry.
//
// Bounded and serial; never retries on ctx cancellation.
func (c *Client) resilientGet(ctx context.Context, url, accept string, bearer bool) ([]byte, int, error) {
	return c.getWith(ctx, url, accept, bearer, true)
}

// getWith is resilientGet with the session recovery on or off.
func (c *Client) getWith(ctx context.Context, url, accept string, bearer, recoverSession bool) ([]byte, int, error) {
	const maxThrottle = 3
	for attempt := 0; ; attempt++ {
		b, st, _, err := c.do(ctx, http.MethodGet, url, accept, bearer)
		if err != nil {
			return b, st, err
		}
		if isThrottled(st, b) && attempt < maxThrottle {
			d := backoff(attempt)
			select {
			case <-ctx.Done():
				return b, st, ctx.Err()
			case <-time.After(d):
			}
			continue
		}
		if bearer && recoverSession && !c.recovered && c.recover != nil && isBankSessionExpired(st, b) {
			c.recovered = true
			if rerr := c.recover(ctx); rerr == nil {
				continue // one retry with the renewed session
			}
			// recovery failed → return the original response; the caller's
			// taxonomy emits the loud re-login instruction.
		}
		return b, st, nil
	}
}

func backoff(attempt int) time.Duration {
	base := time.Duration(2<<attempt) * time.Second // 2s, 4s, 8s
	//nolint:gosec // G404: jitter for retry backoff, not a security/crypto context
	jitter := time.Duration(rand.Int63n(int64(time.Second)))
	return base + jitter
}

// isThrottled = Varnish/edge velocity block (NOT auth death): an HTML body
// "401 V Not Authorized", or a 503. Distinct from the JSON auth 401s.
func isThrottled(status int, body []byte) bool {
	if status == 503 {
		return true
	}
	if status == 401 {
		s := string(body)
		return strings.Contains(s, "Not Authorized") || strings.Contains(s, "401 V")
	}
	return false
}

// isBankSessionExpired = bearer-plane JSON {"code":10006}/{"code":401 JWT
// Token not found} → renewable via Refresh (NOT a dead Chrome session).
func isBankSessionExpired(status int, body []byte) bool {
	s := string(body)
	return strings.Contains(s, `"code":10006`) ||
		(strings.Contains(s, `"code":401`) && strings.Contains(s, "JWT Token not found"))
}

func (c *Client) do(ctx context.Context, method, url, accept string, bearer bool) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("user-agent", c.ua)
	req.Header.Set("accept", accept)
	req.Header.Set("accept-language", "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("origin", "https://clients.boursobank.com")
	req.Header.Set("referer", "https://clients.boursobank.com/")
	if bearer {
		req.Header.Set("authorization", "Bearer "+c.Bearer)
		req.Header.Set("x-referer-feature-id", "_._.web_fr_front_20")
	} else {
		if err := c.ensureCookie(ctx); err != nil {
			return nil, 0, nil, err
		}
		req.Header.Set("cookie", c.cookie)
		req.Header.Set("x-requested-with", "XMLHttpRequest")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return body, resp.StatusCode, resp.Header, nil
}
