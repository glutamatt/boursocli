package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/thomasmarcelin754/boursocli/internal/client"
	"github.com/thomasmarcelin754/boursocli/internal/htmlx"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

const cookieBase = "https://clients.boursobank.com"

// getHTML GETs a cookie-plane path and parses it strictly. path is relative
// (e.g. "/compte/ord/<key>/positions"); _hinclude=1 is appended to fetch the
// bare fragment. Loud on every failure mode — never a
// silent empty:
//   - transport error                         → error
//   - Varnish edge throttle (HTML "401 V …")   → explicit back-off error
//   - dead/login session                       → explicit re-login error
//   - any non-200                              → error with snippet
func getHTML(ctx context.Context, cl *client.Client, path string) (*htmlx.Doc, error) {
	url := cookieBase + path
	if strings.Contains(path, "?") {
		url += "&_hinclude=1"
	} else {
		url += "?_hinclude=1"
	}
	body, status, err := cl.Cookie(ctx, url)
	if err != nil {
		return nil, err
	}
	// Varnish/edge velocity throttle — NOT auth death: an HTML body
	// "401 V Not Authorized". Back off, do not re-auth.
	if status == 401 && (strings.Contains(string(body), "Not Authorized") ||
		strings.Contains(string(body), "401 V")) {
		return nil, fmt.Errorf("throttle en bordure (Varnish 401 V) sur %s — temporiser et réessayer plus lentement ; la session n’est PAS morte, ne pas ré-authentifier", path)
	}
	if status != 200 {
		return nil, fmt.Errorf("%s → HTTP %d : %s", path, status, snippet(body))
	}
	doc, err := htmlx.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.IsLoginRedirect() {
		return nil, fmt.Errorf("%s → page déconnecté/connexion : la session Chrome BoursoBank est morte. Se reconnecter dans Chrome, puis réessayer (la ré-extraction ne répare pas une session morte)", path)
	}
	return doc, nil
}

// getPage is getHTML for a page that must hold its data block (want, e.g.
// the documents table) or an explicit empty state (.c-empty-state). The
// bank sometimes serves such a page without its data block (seen live
// 2026-09-30 on documents pages, not reproduced): retry once after a pause,
// then fail loudly — never a silent empty answer.
func getPage(ctx context.Context, cl *client.Client, path, want string) (*htmlx.Doc, error) {
	for attempt := 0; ; attempt++ {
		doc, err := getHTML(ctx, cl, path)
		if err != nil {
			return nil, err
		}
		if doc.Sel(want).Length() > 0 || doc.Sel(".c-empty-state").Length() > 0 {
			return doc, nil
		}
		title := htmlx.Clean(doc.Sel("title").First().Text())
		if attempt == 1 {
			return nil, fmt.Errorf("%s : la page reçue n’a ni %q ni état vide (titre %q) — la banque a servi une autre page ; réessayer dans un moment", path, want, title)
		}
		out.Debugf("%s : pas de %q (titre %q) — nouvel essai dans 3 s", path, want, title)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
