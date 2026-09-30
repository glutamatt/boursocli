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
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
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
