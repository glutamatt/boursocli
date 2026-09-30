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

func TestWriteNewFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ops.csv")
	if err := writeNewFile(p, []byte("a;b\n")); err != nil {
		t.Fatalf("new file: %v", err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", fi.Mode().Perm())
	}
	// never overwrite
	if err := writeNewFile(p, []byte("other")); err == nil {
		t.Error("existing file overwritten")
	}
	if b, _ := os.ReadFile(p); string(b) != "a;b\n" { //nolint:gosec // G304: test temp file
		t.Errorf("content changed: %q", b)
	}
	// never follow a symlink, even a dangling one
	target := filepath.Join(dir, "elsewhere")
	link := filepath.Join(dir, "link.csv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeNewFile(link, []byte("secret")); err == nil {
		t.Error("write through a symlink accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("symlink target was created")
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
		{"export", "--account", "cav", "--from", "2026-01-01"},
		{"budget-movements", "--account", "cav", "--to", "bad"},
	}
	for _, args := range cases {
		root := buildRoot()
		root.SetArgs(append(args, "--config", cfg, "--quiet"))
		root.SetOut(&strings.Builder{})
		err := root.ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), "invalide") {
			t.Errorf("%v: err = %v, want a validation error", args, err)
		}
		if _, statErr := os.Stat(cfgDir); !os.IsNotExist(statErr) {
			t.Fatalf("%v: a session was opened (config folder created)", args)
		}
	}
}
