package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// privateDir is a 0700 folder: t.TempDir() follows the umask, and the
// config refuses a folder others can read.
func privateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPathOverride(t *testing.T) {
	if p, _ := Path("/x/y.json"); p != "/x/y.json" {
		t.Fatalf("override ignored: %s", p)
	}
	if p, err := Path(""); err != nil || !strings.HasSuffix(p, filepath.Join("boursocli", "config.json")) {
		t.Fatalf("default path wrong: %s %v", p, err)
	}
}

func TestSaveAtomicAndLoad(t *testing.T) {
	dir := privateDir(t)
	p := filepath.Join(dir, "sub", "config.json")
	c := &Config{Bearer: "JWT", UserHash: "h"}
	if err := c.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// file 0600, dir 0700, no leftover .tmp
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file perm = %o, want 600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(p))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %o, want 700", di.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".config-*.tmp")); len(left) != 0 {
		t.Fatalf("temp file not cleaned: %v", left)
	}
	got, err := Load(p)
	if err != nil || got.Bearer != "JWT" || got.Version != Version {
		t.Fatalf("Load roundtrip: %+v %v", got, err)
	}
	// missing file → empty config, no error
	empty, err := Load(filepath.Join(dir, "nope.json"))
	if err != nil || empty.Version != Version || empty.Bearer != "" {
		t.Fatalf("Load(missing): %+v %v", empty, err)
	}
}

func TestRedactedHidesSecrets(t *testing.T) {
	c := &Config{Bearer: "supersecretjwt", UserHash: "abc"}
	r := c.Redacted()
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	for _, leak := range []string{"supersecretjwt", "abc"} {
		if strings.Contains(s, leak) {
			t.Fatalf("Redacted leaked %q: %s", leak, s)
		}
	}
	if !strings.Contains(s, "***") {
		t.Fatalf("expected redaction markers: %s", s)
	}
}

func TestBearerLikelyExpired(t *testing.T) {
	margin := 2 * time.Minute
	// No bearer → expired
	if !(&Config{}).BearerLikelyExpired(margin) {
		t.Fatal("empty bearer must be expired")
	}
	// Bearer with exp in the future → not expired
	future := time.Now().Add(12 * time.Hour).UTC().Format(time.RFC3339)
	c := &Config{Bearer: "jwt", BearerExp: future}
	if c.BearerLikelyExpired(margin) {
		t.Fatal("bearer with 12h remaining must not be expired")
	}
	// Bearer with exp in the past → expired
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	c2 := &Config{Bearer: "jwt", BearerExp: past}
	if !c2.BearerLikelyExpired(margin) {
		t.Fatal("bearer with exp in the past must be expired")
	}
	// Bearer within margin → expired
	soon := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	c3 := &Config{Bearer: "jwt", BearerExp: soon}
	if !c3.BearerLikelyExpired(margin) {
		t.Fatal("bearer expiring within margin must be expired")
	}
	// No BearerExp but BearerSavedAt recent → not expired (fallback)
	c4 := &Config{Bearer: "jwt", BearerSavedAt: time.Now().UTC().Format(time.RFC3339)}
	if c4.BearerLikelyExpired(margin) {
		t.Fatal("recent BearerSavedAt fallback must not be expired")
	}
	// No BearerExp, BearerSavedAt old → expired (fallback)
	c5 := &Config{Bearer: "jwt", BearerSavedAt: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)}
	if !c5.BearerLikelyExpired(margin) {
		t.Fatal("old BearerSavedAt fallback must be expired")
	}
}

// A v1 config held the Chrome cookie jars (incl. rememberme). Loading it
// must remove them from disk at once and keep the rest.
func TestLoadScrubsLegacyCookies(t *testing.T) {
	dir := privateDir(t)
	p := filepath.Join(dir, "config.json")
	v1 := `{"version":1,"chrome_profile":"Default","cookies_by_host":{"clients.boursobank.com":"rememberme=SECRETVAL; sid=1"},"bearer":"JWT"}`
	if err := os.WriteFile(p, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Bearer != "JWT" || c.ChromeProfile != "Default" {
		t.Fatalf("settings lost: %+v", c)
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: test temp file
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRETVAL") || strings.Contains(string(b), "cookies_by_host") {
		t.Fatalf("legacy cookies still on disk: %s", b)
	}
}

func TestForgetSession(t *testing.T) {
	c := &Config{ChromeProfile: "Default", Bearer: "JWT", BearerExp: "x", BearerSavedAt: "y", UserHash: "h", AllowSessionRefresh: true}
	c.ForgetSession()
	if c.Bearer != "" || c.BearerExp != "" || c.BearerSavedAt != "" || c.UserHash != "" {
		t.Fatalf("session not forgotten: %+v", c)
	}
	if c.ChromeProfile != "Default" || !c.AllowSessionRefresh {
		t.Fatalf("non-secret settings lost: %+v", c)
	}
}
