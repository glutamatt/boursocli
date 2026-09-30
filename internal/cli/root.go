// Package cli wires the cobra command tree. Auth = chromecookies dual-domain
// (auto) → scrape bearer; data = Bearer JSON first, cookie-plane fallback.
package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/auth"
	"github.com/thomasmarcelin754/boursocli/internal/client"
	"github.com/thomasmarcelin754/boursocli/internal/config"
	"github.com/thomasmarcelin754/boursocli/internal/out"
	"github.com/thomasmarcelin754/boursocli/internal/version"
)

var (
	flagConfig       string
	flagProfile      string
	flagFormat       string
	flagQuiet        bool
	flagDebug        bool
	flagRefresh      bool // force re-extract cookies + re-scrape bearer
	flagAllowRefresh bool // allow POST session/auth/refresh (extends the bank session)
)

func ExecuteContext(ctx context.Context) error {
	root := buildRoot()
	return root.ExecuteContext(ctx)
}

func buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "boursocli",
		Short:         "CLI agent-first pour un compte BoursoBank personnel (lecture seule).",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			out.Format, out.Quiet, out.Debug = flagFormat, flagQuiet, flagDebug
			return nil
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	pf := root.PersistentFlags()
	pf.StringVar(&flagConfig, "config", "", "chemin de config (défaut : dossier config de l’OS)")
	pf.StringVar(&flagProfile, "chrome-profile", "", "nom/chemin du profil Chrome (défaut : Default, ou config)")
	pf.StringVar(&flagFormat, "format", "json", "sortie : json (agent-first) | table")
	pf.BoolVar(&flagQuiet, "quiet", false, "supprime les diagnostics stderr")
	pf.BoolVar(&flagDebug, "debug", false, "diagnostics stderr verbeux")
	pf.BoolVar(&flagRefresh, "refresh", false, "force la ré-extraction des cookies + re-scrape du bearer")
	pf.BoolVar(&flagAllowRefresh, "allow-session-refresh", false, "autorise POST session/auth/refresh (prolonge la session à la banque ; désactivé par défaut)")

	root.AddCommand(
		newConfigCmd(), newAccountsCmd(),
		newOperationsCmd(), newTransfersCmd(), newBudgetsCmd(), newIncidentsCmd(),
		newPositionsCmd(), newOrdOrdersCmd(), newOrdFiscaliteCmd(), newOrdMouvementsCmd(),
		newDocumentsCmd(), newOrdOstCmd(), newBudgetMovementsCmd(),
		newCardCmd(), newSepaCmd(), newQuoteCmd(),
		newOrderbookCmd(), newTopflopCmd(), newMessagesCmd(),
		newProfileCmd(), newRecipientsCmd(),
		newDocsCmd(), newVersionCmd(),
	)
	return root
}

// session loads config and returns a ready client. Read-only by
// construction:
//   - the Chrome cookie jars are read on demand and kept in memory only;
//   - a stored bearer is reused while the bank accepts it (Probe); when it
//     is rejected or near expiry, a new one comes from the live Chrome
//     session (GET dashboard) — the CLI never extends the session itself;
//   - POST session/auth/refresh is sent only if the owner allowed it
//     (--allow-session-refresh / config allow_session_refresh).
func session(ctx context.Context) (*client.Client, *config.Config, string, error) {
	cfgPath, err := config.Path(flagConfig)
	if err != nil {
		return nil, nil, "", err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, "", err
	}
	if flagProfile != "" {
		cfg.ChromeProfile, cfg.ChromeProfileAuto = flagProfile, false
	}
	allowRefresh := flagAllowRefresh || cfg.AllowSessionRefresh

	c := client.New("", cfg.HTTPUserAgent)
	c.AllowRefresh(allowRefresh)
	c.SetCookieSource(func(ctx context.Context) (string, error) {
		return loadCookies(ctx, cfg, cfgPath)
	})
	// bootstrap = a new bearer from the live Chrome session: fresh cookies,
	// then GET dashboard. When an auto-picked profile no longer holds a
	// live session, drop that pin and scan the profiles once more.
	bootstrap := func(ctx context.Context) error {
		c.SetCookie("")
		err := c.Bootstrap(ctx)
		if err != nil && cfg.ChromeProfileAuto {
			out.Logf("profil Chrome auto-épinglé %q sans session vivante — nouveau scan des profils", cfg.ChromeProfile)
			cfg.ChromeProfile, cfg.ChromeProfileAuto = "", false
			c.SetCookie("")
			err = c.Bootstrap(ctx)
		}
		if err != nil {
			return err
		}
		cfg.Bearer, cfg.UserHash = c.Bearer, c.UserHash
		cfg.BearerSavedAt = time.Now().UTC().Format(time.RFC3339)
		cfg.BearerExp = ""
		if exp := c.BearerExp(); !exp.IsZero() {
			cfg.BearerExp = exp.Format(time.RFC3339)
		}
		return cfg.Save(cfgPath)
	}
	// Mid-command expiry: the (opt-in) refresh keeps the server session,
	// but the bearer must still be scraped again.
	c.SetRecover(func(ctx context.Context) error {
		if allowRefresh {
			if err := c.Refresh(ctx); err != nil {
				out.Debugf("refresh refusé (%v) — nouveau bearer depuis Chrome", err)
			}
		}
		return bootstrap(ctx)
	})

	needBootstrap := flagRefresh || cfg.BearerLikelyExpired(2*time.Minute)
	if !needBootstrap {
		c.Bearer, c.UserHash = cfg.Bearer, cfg.UserHash
		// A re-login in Chrome kills the old server session even though the
		// JWT exp is still in the future. Probe cheaply. Only an auth
		// refusal leads to the Chrome cookie store; any other failure
		// (network, throttle, 404/5xx on the probe endpoint) keeps the
		// stored bearer — the command itself reports real problems, and a
		// real expiry there goes through recover.
		if err := c.Probe(ctx); err != nil {
			if errors.Is(err, client.ErrBearerRejected) {
				out.Debugf("bearer en config rejeté (%v) — nouveau bearer depuis Chrome", err)
				needBootstrap = true
			} else {
				out.Debugf("probe sans verdict (%v) — bearer en config conservé", err)
			}
		}
	}
	if needBootstrap {
		if err := bootstrap(ctx); err != nil {
			return nil, nil, "", err
		}
	}
	return c, cfg, cfgPath, nil
}

// loadCookies reads both jars from Chrome. With no profile set, the
// auto-pick chooses one and it is pinned (marked auto) so the scan runs
// once. A failing auto pin is dropped and the scan runs again.
func loadCookies(ctx context.Context, cfg *config.Config, cfgPath string) (string, error) {
	out.Logf("extraction des cookies BoursoBank depuis Chrome (profil %q, bi-domaine)…", orDefault(cfg.ChromeProfile))
	ex, err := auth.ExtractCookies(ctx, cfg.ChromeProfile, os.Stderr)
	if err != nil && cfg.ChromeProfileAuto {
		out.Logf("profil Chrome auto-épinglé %q illisible (%v) — nouveau scan des profils", cfg.ChromeProfile, err)
		cfg.ChromeProfile, cfg.ChromeProfileAuto = "", false
		ex, err = auth.ExtractCookies(ctx, "", os.Stderr)
	}
	if err != nil {
		return "", err
	}
	if cfg.ChromeProfile == "" && ex.Profile != "" {
		cfg.ChromeProfile, cfg.ChromeProfileAuto = ex.Profile, true
		if err := cfg.Save(cfgPath); err != nil {
			return "", err
		}
	}
	return auth.MergedHeader(ex.CookiesByHost), nil
}

func orDefault(s string) string {
	if s == "" {
		return "auto (multi-profil : profil bourso le plus frais)"
	}
	return s
}
