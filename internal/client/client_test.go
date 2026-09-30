package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// allow points the egress policy at test servers (plain http, loopback
// host:port) for the duration of the test.
func allow(t *testing.T, srvs ...*httptest.Server) {
	t.Helper()
	old := egress
	p := policy{scheme: "http", hosts: map[string]bool{}}
	for _, s := range srvs {
		u, err := url.Parse(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		p.hosts[u.Host] = true
	}
	egress = p
	t.Cleanup(func() { egress = old })
}

func mkJWT(claims map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	pl, _ := json.Marshal(claims) //nolint:errchkjson // test helper, map[string]any literals never fail
	return hdr + "." + base64.RawURLEncoding.EncodeToString(pl) + ".sig"
}

func TestBearerFromCookie(t *testing.T) {
	good := mkJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "userHash": "H123"})
	c := New("a=1; brsxds_deadbeefdeadbeefdeadbeefdeadbeef="+good+"; b=2", "")
	if jwt, uh, ok := c.bearerFromCookie(); !ok || uh != "H123" || jwt != good {
		t.Fatalf("valid brsxds JWT not accepted: ok=%v uh=%q", ok, uh)
	}
	// expired → declined
	exp := mkJWT(map[string]any{"exp": float64(time.Now().Add(-time.Hour).Unix()), "userHash": "H"})
	if _, _, ok := New("brsxds_x="+exp, "").bearerFromCookie(); ok {
		t.Fatal("expired brsxds JWT must be declined")
	}
	// no userHash claim → declined
	nouh := mkJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())})
	if _, _, ok := New("brsxds_x="+nouh, "").bearerFromCookie(); ok {
		t.Fatal("brsxds JWT without userHash must be declined")
	}
	// absent / malformed → declined (no regression to existing error path)
	if _, _, ok := New("sid=1; other=2", "").bearerFromCookie(); ok {
		t.Fatal("no brsxds cookie must yield ok=false")
	}
	if _, _, ok := New("brsxds_x=notajwt", "").bearerFromCookie(); ok {
		t.Fatal("malformed brsxds value must yield ok=false")
	}
}

func TestPredicates(t *testing.T) {
	if !isThrottled(503, nil) || !isThrottled(401, []byte("401 V Not Authorized")) {
		t.Fatal("throttle not detected")
	}
	if isThrottled(200, []byte("ok")) || isThrottled(401, []byte(`{"code":401}`)) {
		t.Fatal("false throttle")
	}
	if !isBankSessionExpired(401, []byte(`{"code":10006,"message":"x"}`)) ||
		!isBankSessionExpired(401, []byte(`{"code":401,"message":"JWT Token not found"}`)) {
		t.Fatal("bank expiry not detected")
	}
	if isBankSessionExpired(200, []byte(`{"ok":true}`)) {
		t.Fatal("false bank expiry")
	}
	if d := backoff(0); d <= 0 {
		t.Fatal("backoff non-positive")
	}
}

func TestDoHeaders(t *testing.T) {
	var gotAuth, gotCookie, gotXRW string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		gotCookie = r.Header.Get("cookie")
		gotXRW = r.Header.Get("x-requested-with")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	allow(t, srv)
	c := New("ck=1", "")
	if c.ua != defaultUA {
		t.Fatal("default UA not applied")
	}
	c.Bearer = "JWT"

	_, st, _, err := c.do(context.Background(), http.MethodGet, srv.URL, "application/json", true)
	if err != nil || st != 200 {
		t.Fatalf("bearer do: %v st=%d", err, st)
	}
	if gotAuth != "Bearer JWT" || gotCookie != "" || gotXRW != "" {
		t.Fatalf("bearer headers wrong: auth=%q cookie=%q", gotAuth, gotCookie)
	}
	_, _, _, _ = c.do(context.Background(), http.MethodGet, srv.URL, "text/html", false)
	if gotCookie != "ck=1" || gotXRW != "XMLHttpRequest" {
		t.Fatalf("cookie headers wrong: cookie=%q xrw=%q", gotCookie, gotXRW)
	}
}

func TestResilientThrottleThenSuccess(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable) // throttle once
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	allow(t, srv)
	c := New("ck=1", "")
	// backoff(0) ~2s; keep the test fast by shrinking via a tiny ctx is not
	// possible — instead assert it recovers (2s is acceptable for one case).
	b, st, err := c.resilientGet(context.Background(), srv.URL, "application/json", false)
	if err != nil || st != 200 || !strings.Contains(string(b), "ok") {
		t.Fatalf("did not recover from throttle: st=%d err=%v", st, err)
	}
	if atomic.LoadInt32(&n) < 2 {
		t.Fatal("did not retry after throttle")
	}
}

func TestResilientBankExpiryRefreshRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "session/auth/refresh") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":10006,"message":"Votre session a expirée"}`))
			return
		}
		_, _ = w.Write([]byte(`{"recovered":true}`))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase = srv.URL // seam: Refresh() targets apiBase
	defer func() { apiBase = "https://api.boursobank.com/services/api/v1.7" }()
	c := New("ck=1", "")
	c.Bearer = "JWT"
	c.AllowRefresh(true)
	c.SetRecover(c.Refresh)
	b, st, err := c.resilientGet(context.Background(), srv.URL, "application/json", true)
	if err != nil || st != 200 || !strings.Contains(string(b), "recovered") {
		t.Fatalf("refresh+retry failed: st=%d err=%v body=%s", st, err, b)
	}
}

func TestRefreshAndBootstrap(t *testing.T) {
	html := `garbage "USER_HASH":"abc123" more "DEFAULT_API_BEARER":"JWT.tok" end`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "refresh") {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase, dashboard = srv.URL, srv.URL+"/"
	defer func() {
		apiBase = "https://api.boursobank.com/services/api/v1.7"
		dashboard = "https://clients.boursobank.com/"
	}()
	c := New("ck=1", "")
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh must be refused while not allowed")
	}
	c.AllowRefresh(true)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := c.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if c.Bearer != "JWT.tok" || c.UserHash != "abc123" {
		t.Fatalf("scrape wrong: bearer=%q hash=%q", c.Bearer, c.UserHash)
	}
}

func TestBootstrapDeadSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>connexion</html>`)) // no bearer blob
	}))
	defer srv.Close()
	allow(t, srv)
	dashboard = srv.URL + "/"
	defer func() { dashboard = "https://clients.boursobank.com/" }()
	c := New("ck=1", "")
	if err := c.Bootstrap(context.Background()); err == nil {
		t.Fatal("expected loud error on dead session")
	}
}

func TestProbeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "timeline/unreadcount") {
			t.Fatalf("Probe hit unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"count":0}`))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase = srv.URL
	defer func() { apiBase = "https://api.boursobank.com/services/api/v1.7" }()
	c := New("ck=1", "")
	c.Bearer, c.UserHash = "JWT", "H1"
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe should succeed on 200: %v", err)
	}
}

func TestProbeFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":10006}`))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase = srv.URL
	defer func() { apiBase = "https://api.boursobank.com/services/api/v1.7" }()
	c := New("ck=1", "")
	c.Bearer, c.UserHash = "JWT", "H1"
	if err := c.Probe(context.Background()); err == nil {
		t.Fatal("Probe should fail on 401")
	}
}

func TestPublicAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "_public_/feed/instrument/quote") {
			t.Fatalf("PublicAPI hit unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("authorization") == "" {
			t.Fatal("PublicAPI should send bearer")
		}
		_, _ = w.Write([]byte(`{"symbol":"1rPENGI","last":27.25}`))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase = srv.URL
	defer func() { apiBase = "https://api.boursobank.com/services/api/v1.7" }()
	c := New("ck=1", "")
	c.Bearer = "JWT"
	b, st, err := c.PublicAPI(context.Background(), "_public_/feed/instrument/quote/1rPENGI")
	if err != nil || st != 200 {
		t.Fatalf("PublicAPI failed: st=%d err=%v", st, err)
	}
	if !strings.Contains(string(b), "1rPENGI") {
		t.Fatalf("unexpected body: %s", b)
	}
}

func TestBearerExp(t *testing.T) {
	c := New("", "")
	c.Bearer = mkJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "userHash": "H"})
	exp := c.BearerExp()
	if exp.IsZero() || time.Until(exp) < 50*time.Minute {
		t.Fatalf("BearerExp should be ~1h from now, got %v", exp)
	}
	c.Bearer = "garbage"
	if !c.BearerExp().IsZero() {
		t.Fatal("bad JWT should yield zero time")
	}
}

func TestAPICookieWrappers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase = srv.URL
	defer func() { apiBase = "https://api.boursobank.com/services/api/v1.7" }()
	c := New("ck=1", "")
	c.Bearer, c.UserHash = "JWT", "H1"
	if b, st, err := c.API(context.Background(), "test/resource"); err != nil || st != 200 || !strings.Contains(string(b), "ok") {
		t.Fatalf("API wrapper failed: st=%d err=%v", st, err)
	}
	if b, st, err := c.Cookie(context.Background(), srv.URL+"/page"); err != nil || st != 200 || !strings.Contains(string(b), "ok") {
		t.Fatalf("Cookie wrapper failed: st=%d err=%v", st, err)
	}
	if b, st, err := c.CookieOnce(context.Background(), srv.URL+"/once"); err != nil || st != 200 || !strings.Contains(string(b), "ok") {
		t.Fatalf("CookieOnce wrapper failed: st=%d err=%v", st, err)
	}
}

func TestRedirectToUntrustedHostRefused(t *testing.T) {
	// untrusted redirect target must receive NOTHING (not even a cookie-less request)
	var hit int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		_, _ = w.Write([]byte("x"))
	}))
	defer evil.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusFound)
	}))
	defer src.Close()
	allow(t, src)
	c := New("secret=1", "")
	if _, _, _, err := c.do(context.Background(), http.MethodGet, src.URL, "text/html", false); err == nil {
		t.Fatal("redirect to a host outside the policy must fail")
	}
	if atomic.LoadInt32(&hit) != 0 {
		t.Fatal("a request reached the untrusted redirect host")
	}
}

func TestRedirectKeepsPlanes(t *testing.T) {
	var gotCookie, gotAuth string
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie, gotAuth = r.Header.Get("cookie"), r.Header.Get("authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+"/next", http.StatusFound)
	}))
	defer src.Close()
	allow(t, src, dst)
	c := New("secret=1", "")
	c.Bearer = "JWT"

	// cookie plane: the documented cross-host export chain keeps the cookie
	if _, st, _, err := c.do(context.Background(), http.MethodGet, src.URL, "text/html", false); err != nil || st != 200 {
		t.Fatalf("cookie-plane redirect: st=%d err=%v", st, err)
	}
	if gotCookie != "secret=1" {
		t.Fatalf("cookie-plane redirect lost the cookie: %q", gotCookie)
	}
	// bearer plane: a redirect must never ADD the cookie jar
	if _, st, _, err := c.do(context.Background(), http.MethodGet, src.URL, "application/json", true); err != nil || st != 200 {
		t.Fatalf("bearer-plane redirect: st=%d err=%v", st, err)
	}
	if gotCookie != "" {
		t.Fatalf("bearer-plane redirect gained the cookie jar: %q", gotCookie)
	}
	_ = gotAuth // Go's own policy decides for Authorization across hosts
}

func TestGuardMethods(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	allow(t, srv)
	apiBase = srv.URL + "/services/api/v1.7"
	defer func() { apiBase = "https://api.boursobank.com/services/api/v1.7" }()
	c := New("ck=1", "")
	ctx := context.Background()

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if _, _, _, err := c.do(ctx, m, srv.URL+"/x", "application/json", true); err == nil {
			t.Errorf("%s must be refused", m)
		}
	}
	// the refresh POST: refused until allowed, then only on its exact path
	if _, _, _, err := c.do(ctx, http.MethodPost, apiBase+refreshPath, "application/json", false); err == nil {
		t.Error("refresh POST must be refused by default")
	}
	c.AllowRefresh(true)
	if _, _, _, err := c.do(ctx, http.MethodPost, apiBase+refreshPath, "application/json", false); err != nil {
		t.Errorf("allowed refresh POST refused: %v", err)
	}
	if _, _, _, err := c.do(ctx, http.MethodPost, apiBase+"/_user_/x/bank/cashtransfer", "application/json", false); err == nil {
		t.Error("allowing refresh must not allow any other POST")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server saw %d requests, want exactly 1 (the allowed refresh)", n)
	}
}

func TestEgressPolicy(t *testing.T) {
	ok := []string{
		"https://api.boursobank.com/services/api/v1.7/_user_/_H_/bank/account/accounts?_host=clients.boursobank.com",
		"https://clients.boursobank.com/budget/exporter-mouvements/abc?movementSearch%5BfromDate%5D=01%2F01%2F2026",
		"https://clients.boursorama.com/",
		"https://CLIENTS.BOURSOBANK.COM/",
	}
	bad := []string{
		"http://clients.boursobank.com/",            // cleartext
		"https://clients.boursobank.com:8443/",      // other port
		"https://www.boursorama.com/",               // not allow-listed
		"https://evil.boursorama.com/",              // no suffix match
		"https://boursobank.com.evil.example/",      // lookalike
		"https://user:pw@clients.boursobank.com/",   // userinfo
		"https://api.boursobank.com/a/../b",         // dot segment
		"https://api.boursobank.com/a/%2e%2e/b",     // encoded dot segment
		"https://api.boursobank.com/a%2Fb",          // encoded slash
		"https://api.boursobank.com/a\\b",           // backslash
		"https://api.boursobank.com/a#frag",         // fragment
		"https://api.boursobank.com/services/./api", // single dot
	}
	for _, raw := range ok {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := egress.check(u); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
	for _, raw := range bad {
		u, err := url.Parse(raw)
		if err != nil {
			continue // unparseable never leaves either
		}
		if err := egress.check(u); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
}

func TestProbeClassification(t *testing.T) {
	cases := []struct {
		st       int
		body     string
		rejected bool
	}{
		{401, `{"code":10006}`, true},
		{401, `{"code":401,"message":"JWT Token not found"}`, true},
		{401, `401 V Not Authorized`, false}, // edge throttle, not auth
		{503, ``, false},
		{500, `oops`, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.st)
			_, _ = w.Write([]byte(tc.body))
		}))
		allow(t, srv)
		apiBase = srv.URL
		c := New("", "")
		c.Bearer, c.UserHash = "JWT", "H1"
		err := c.Probe(context.Background())
		if err == nil {
			t.Errorf("HTTP %d %q: Probe succeeded", tc.st, tc.body)
		} else if got := errors.Is(err, ErrBearerRejected); got != tc.rejected {
			t.Errorf("HTTP %d %q: rejected=%v, want %v (%v)", tc.st, tc.body, got, tc.rejected, err)
		}
		srv.Close()
	}
	apiBase = "https://api.boursobank.com/services/api/v1.7"
}

func TestCookieSourceIsLazy(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("cookie")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	allow(t, srv)
	var loads int32
	c := New("", "")
	c.Bearer = "JWT"
	c.SetCookieSource(func(context.Context) (string, error) {
		atomic.AddInt32(&loads, 1)
		return "jar=1", nil
	})
	ctx := context.Background()
	_, _, _, _ = c.do(ctx, http.MethodGet, srv.URL, "application/json", true)
	if atomic.LoadInt32(&loads) != 0 {
		t.Fatal("a bearer-plane request loaded the Chrome cookies")
	}
	_, _, _, _ = c.do(ctx, http.MethodGet, srv.URL, "text/html", false)
	_, _, _, _ = c.do(ctx, http.MethodGet, srv.URL, "text/html", false)
	if atomic.LoadInt32(&loads) != 1 || gotCookie != "jar=1" {
		t.Fatalf("cookie source: loads=%d cookie=%q, want 1 load and the jar", loads, gotCookie)
	}
}
