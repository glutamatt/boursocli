package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/config"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Affiche/gère la config du CLI"}
	c.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Affiche la config (secrets masqués)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := config.Path(flagConfig)
			if err != nil {
				return out.Fail(err)
			}
			cfg, err := config.Load(p)
			if err != nil {
				return out.Fail(err)
			}
			return out.Data(cfg.Redacted())
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "set <clé> <valeur>",
		Short: "Règle chrome_profile (nom ou chemin) ou allow_session_refresh (true|false)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := config.Path(flagConfig)
			if err != nil {
				return out.Fail(err)
			}
			cfg, err := config.Load(p)
			if err != nil {
				return out.Fail(err)
			}
			switch args[0] {
			case "chrome_profile":
				cfg.ChromeProfile = args[1]
			case "allow_session_refresh":
				on, err := strconv.ParseBool(args[1])
				if err != nil {
					return out.Fail(fmt.Errorf("allow_session_refresh : true ou false attendu, pas %q", args[1]))
				}
				cfg.AllowSessionRefresh = on
			default:
				return out.Fail(fmt.Errorf("clé inconnue %q (supportées : chrome_profile, allow_session_refresh)", args[0]))
			}
			if err := cfg.Save(p); err != nil {
				return out.Fail(err)
			}
			return out.Data(map[string]string{args[0]: args[1]})
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "wipe",
		Short: "Efface le bearer et le user hash du disque (les réglages restent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := config.Path(flagConfig)
			if err != nil {
				return out.Fail(err)
			}
			cfg, err := config.Load(p)
			if err != nil {
				return out.Fail(err)
			}
			cfg.ForgetSession()
			if err := cfg.Save(p); err != nil {
				return out.Fail(err)
			}
			return out.OK("wipe", map[string]any{"config": p})
		},
	})
	return c
}
