//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRefusesUnsafeFiles(t *testing.T) {
	dir := privateDir(t)

	loose := filepath.Join(dir, "loose.json")
	if err := os.WriteFile(loose, []byte(`{"bearer":"JWT"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil { //nolint:gosec // G302: the unsafe mode under test
		t.Fatal(err)
	}
	if _, err := Load(loose); err == nil || !strings.Contains(err.Error(), "trop larges") {
		t.Fatalf("0644 config accepted: %v", err)
	}

	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlinked config accepted")
	}

	shared := filepath.Join(dir, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil { //nolint:gosec // G302: the unsafe mode under test
		t.Fatal(err)
	}
	if err := (&Config{Bearer: "JWT"}).Save(filepath.Join(shared, "config.json")); err == nil {
		t.Fatal("Save into a world-writable folder accepted")
	}
	if _, err := Load(filepath.Join(shared, "config.json")); err == nil {
		t.Fatal("Load from a world-writable folder accepted")
	}
}

// Folders others can read but not write are fine (the file is 0600): the v1
// config folder (0750), a 0755 project folder, and sticky /tmp-like folders.
func TestFolderRules(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o750, 0o755, os.ModeSticky | 0o777} {
		d := filepath.Join(t.TempDir(), "cfg")
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, mode); err != nil { //nolint:gosec // G302: the modes under test
			t.Fatal(err)
		}
		p := filepath.Join(d, "config.json")
		if err := (&Config{Bearer: "JWT"}).Save(p); err != nil {
			t.Errorf("mode %o: Save refused: %v", mode, err)
			continue
		}
		if c, err := Load(p); err != nil || c.Bearer != "JWT" {
			t.Errorf("mode %o: Load: %+v %v", mode, c, err)
		}
	}
}

// Upgrade from v1: 0750 folder, cookies in the file, npm cache next to it.
func TestUpgradeFromV1(t *testing.T) {
	d := filepath.Join(t.TempDir(), "boursocli")
	if err := os.MkdirAll(filepath.Join(d, "ck-cache", "node_modules", "@steipete", "sweet-cookie"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o750); err != nil { //nolint:gosec // G302: the v1 mode under test
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "ck-cache", "node_modules", "@steipete", "sweet-cookie", "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "config.json")
	v1 := `{"version":1,"cookies_by_host":{"clients.boursobank.com":"rememberme=SECRETVAL"},"bearer":"JWT"}`
	if err := os.WriteFile(p, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load after upgrade: %v", err)
	}
	if c.Bearer != "JWT" || c.Version != Version {
		t.Fatalf("config after upgrade: %+v", c)
	}
	b, _ := os.ReadFile(p) //nolint:gosec // G304: test temp file
	if strings.Contains(string(b), "SECRETVAL") {
		t.Fatalf("v1 cookies still on disk: %s", b)
	}
	if _, err := os.Stat(filepath.Join(d, "ck-cache")); !os.IsNotExist(err) {
		t.Fatal("v1 npm cache not removed")
	}
}

// A pre-planted temp name (the v1 fixed "<path>.tmp") must not receive the
// secrets: Save uses a random O_EXCL name.
func TestSaveIgnoresPlantedTmp(t *testing.T) {
	dir := privateDir(t)
	p := filepath.Join(dir, "config.json")
	spy := filepath.Join(dir, "spy")
	if err := os.WriteFile(spy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(spy, p+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{Bearer: "SECRETJWT"}).Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, _ := os.ReadFile(spy) //nolint:gosec // G304: test temp file
	if strings.Contains(string(b), "SECRETJWT") {
		t.Fatal("secrets written through the planted symlink")
	}
}
