package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidators(t *testing.T) {
	for _, v := range []string{"1rPENGI", "1rPCAC", "FR0013412020", "1rTCW8", "a.b-c_d", "$INDU", "$COMPX"} {
		if err := validSymbol("--symbol", v); err != nil {
			t.Errorf("symbol %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"", "..", "../x", "a/b", "a?b", "a#b", "a%2e", ".a", "-a", "a b", "$", "$$A", "a$b", strings.Repeat("a", 33)} {
		if err := validSymbol("--symbol", v); err == nil {
			t.Errorf("symbol %q accepted", v)
		}
	}
	for _, v := range []string{"0123456789abcdef0123456789abcdef", "abc_DEF-1"} {
		if err := validID("id", v); err != nil {
			t.Errorf("id %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"", "a/b", "..", "a.b", "a?x=1"} {
		if err := validID("id", v); err == nil {
			t.Errorf("id %q accepted", v)
		}
	}
	for _, v := range []string{"2021", "2026", "1999"} {
		if err := validYear("--year", v); err != nil {
			t.Errorf("year %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"26", "20266", "2026&x=1", "abcd"} {
		if err := validYear("--year", v); err == nil {
			t.Errorf("year %q accepted", v)
		}
	}
	if err := validDateFlags("01/01/2026", "31/12/2026"); err != nil {
		t.Errorf("valid dates refused: %v", err)
	}
	if err := validDateFlags("", ""); err != nil {
		t.Errorf("empty (default) dates refused: %v", err)
	}
	for _, v := range []string{"2026-01-01", "32/01/2026", "01/01/2026&x=1"} {
		if err := validDateFlags(v, ""); err == nil {
			t.Errorf("date %q accepted", v)
		}
	}
}

// A hostile path in an argument must be refused before the command opens a
// session: no config read, no Chrome cookie store, no request.
func TestInjectionRefusedBeforeSession(t *testing.T) {
	cfgDir := filepath.Join(t.TempDir(), "cfg")
	cfg := filepath.Join(cfgDir, "config.json")
	cases := [][]string{
		{"quote", "--symbol", "../../_user_/_H_/bank/account/accounts"},
		{"orderbook", "--symbol", "x?y"},
		{"topflop", "--index", "a/../b"},
		{"budgets", "--id", "../x"},
		{"docs", "--section", "ifu", "--year", "2025&x=1"},
		{"ord-fiscalite", "--account", "ord", "--year", "x"},
		{"budget-movements", "--account", "cav", "--from", "2026-01-01"},
		{"budget-movements", "--account", "cav", "--to", "bad"},
		{"ord-mouvements", "--account", "pea", "--period", "8-2026&form[x]=1"},
		{"docs", "--section", "bourse", "--from", "1/1/26"},
		{"download", "--url", "https://api.boursobank.com/services/api/v1.7/_user_/_H_/bank/cashtransfer", "--out", "x.pdf"},
	}
	for _, args := range cases {
		root := buildRoot()
		root.SetArgs(append(args, "--config", cfg, "--quiet"))
		root.SetOut(&strings.Builder{})
		err := root.ExecuteContext(context.Background())
		if err == nil || (!strings.Contains(err.Error(), "invalide") && !strings.Contains(err.Error(), "refusée")) {
			t.Errorf("%v: err = %v, want a validation error", args, err)
		}
		if _, statErr := os.Stat(cfgDir); !os.IsNotExist(statErr) {
			t.Fatalf("%v: a session was opened (config folder created)", args)
		}
	}
}
