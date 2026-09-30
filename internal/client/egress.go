package client

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// refreshPath is the one endpoint that may receive a non-GET request.
const refreshPath = "/_public_/session/auth/refresh"

var errRefreshDisabled = errors.New("renouvellement de session désactivé (lecture seule) — se reconnecter dans Chrome, ou autoriser explicitement : --allow-session-refresh / `config set allow_session_refresh true`")

// policy is the complete list of places the client may talk to.
type policy struct {
	scheme string
	// hosts are URL authorities (host, or host:port) matched exactly. No
	// suffix match: a CNAME'd or taken-over subdomain of the big legacy
	// boursorama.com zone must never receive the bank session.
	hosts map[string]bool
}

// var (not const) so tests can point it at an httptest server — the same
// seam as apiBase/dashboard.
var egress = policy{
	scheme: "https",
	hosts: map[string]bool{
		"clients.boursobank.com": true,
		"api.boursobank.com":     true,
		"clients.boursorama.com": true,
	},
}

// check refuses a URL outside the policy, or a path the server could
// normalise into another endpoint than the one the command asked for.
func (p policy) check(u *url.URL) error {
	if u.Scheme != p.scheme {
		return fmt.Errorf("sortie refusée : schéma %q (seul %s est autorisé) vers %s", u.Scheme, p.scheme, u.Host)
	}
	if u.User != nil {
		return fmt.Errorf("sortie refusée : identifiants dans l’URL vers %s", u.Host)
	}
	host := strings.ToLower(u.Host)
	if p.scheme == "https" {
		host = strings.TrimSuffix(host, ":443") // the default port, written out
	}
	if !p.hosts[host] {
		return fmt.Errorf("sortie refusée : hôte %q hors liste blanche (BoursoBank uniquement)", u.Host)
	}
	return checkPath(u)
}

// checkPath rejects dot segments, encoded bytes in the path, backslashes,
// control characters and fragments. BoursoBank paths never need them, and
// each one is a way to turn "GET this resource" into "GET another one".
func checkPath(u *url.URL) error {
	if u.Fragment != "" || u.RawFragment != "" {
		return fmt.Errorf("sortie refusée : fragment dans l’URL %s", u.Redacted())
	}
	if u.RawPath != "" || strings.Contains(u.Path, "%") {
		return fmt.Errorf("sortie refusée : caractère encodé dans le chemin %q", u.EscapedPath())
	}
	for _, r := range u.Path {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ' ' {
			return fmt.Errorf("sortie refusée : caractère interdit dans le chemin %q", u.EscapedPath())
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("sortie refusée : segment %q dans le chemin %q", seg, u.Path)
		}
	}
	return nil
}

// guard is the client's only way out. It sees every request, and every
// redirect hop, before any byte is sent: the policy applies, and the method
// is GET — except the session-refresh POST when the owner allowed it.
type guard struct {
	next http.RoundTripper
	c    *Client
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := egress.check(req.URL); err != nil {
		return nil, err
	}
	switch {
	case req.Method == http.MethodGet:
	case req.Method == http.MethodPost && g.c.allowRefresh &&
		req.URL.Host == apiHost() && req.URL.Path == apiPath()+refreshPath:
	default:
		return nil, fmt.Errorf("sortie refusée : méthode %s vers %s (le CLI ne fait que lire)", req.Method, req.URL.Redacted())
	}
	return g.next.RoundTrip(req)
}

func apiHost() string {
	u, err := url.Parse(apiBase)
	if err != nil {
		return ""
	}
	return u.Host
}

func apiPath() string {
	u, err := url.Parse(apiBase)
	if err != nil {
		return ""
	}
	return u.Path
}
