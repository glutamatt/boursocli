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
		newDocumentsCmd(), newOrdOstCmd(), newBudgetMovementsCmd(), newExportCmd(),
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
		cfg.ChromeProfile = flagProfile
	}
	allowRefresh := flagAllowRefresh || cfg.AllowSessionRefresh

	c := client.New("", cfg.HTTPUserAgent)
	c.AllowRefresh(allowRefresh)
	c.SetCookieSource(func(ctx context.Context) (string, error) {
		out.Logf("extraction des cookies BoursoBank depuis Chrome (profil %q, bi-domaine)…", orDefault(cfg.ChromeProfile))
		ex, err := auth.ExtractCookies(ctx, cfg.ChromeProfile, os.Stderr)
		if err != nil {
			return "", err
		}
		if cfg.ChromeProfile == "" && ex.Profile != "" {
			// Pin the auto-picked profile: the all-profile scan runs once.
			cfg.ChromeProfile = ex.Profile
			_ = cfg.Save(cfgPath)
		}
		return auth.MergedHeader(ex.CookiesByHost), nil
	})
	// bootstrap = a new bearer from the live Chrome session: fresh cookies,
	// then GET dashboard.
	bootstrap := func(ctx context.Context) error {
		c.SetCookie("")
		if err := c.Bootstrap(ctx); err != nil {
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
	if allowRefresh {
		c.SetRecover(c.Refresh)
	} else {
		c.SetRecover(bootstrap)
	}

	needBootstrap := flagRefresh || cfg.BearerLikelyExpired(2*time.Minute)
	if !needBootstrap {
		c.Bearer, c.UserHash = cfg.Bearer, cfg.UserHash
		// A re-login in Chrome kills the old server session even though the
		// JWT exp is still in the future. Probe cheaply; only an auth
		// refusal leads to the Chrome cookie store — a network error or a
		// throttle is reported as is.
		if err := c.Probe(ctx); err != nil {
			if !errors.Is(err, client.ErrBearerRejected) {
				return nil, nil, "", err
			}
			out.Debugf("bearer en config rejeté (%v) — nouveau bearer depuis Chrome", err)
			needBootstrap = true
		}
	}
	if needBootstrap {
		if err := bootstrap(ctx); err != nil {
			return nil, nil, "", err
		}
	}
	return c, cfg, cfgPath, nil
}

func orDefault(s string) string {
	if s == "" {
		return "auto (multi-profil : profil bourso le plus frais)"
	}
	return s
}
