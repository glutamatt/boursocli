package auth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1" //nolint:gosec // Chromium's own KDF for v10 cookies; test fixture only
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The embedded sweet-cookie must be exactly the reviewed files listed in
// SHA256SUMS (which scripts/verify-sweetcookie.sh checks against npm).
func TestVendoredIntegrity(t *testing.T) {
	files, err := vendoredFiles()
	if err != nil {
		t.Fatal(err)
	}
	sums, ok := files["SHA256SUMS"]
	if !ok {
		t.Fatal("SHA256SUMS missing from the embedded folder")
	}
	want := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			t.Fatalf("bad SHA256SUMS line %q", sc.Text())
		}
		want[f[1]] = f[0]
	}
	if len(want) == 0 {
		t.Fatal("SHA256SUMS is empty")
	}
	for name, b := range files {
		if name == "SHA256SUMS" || name == "VENDOR.md" {
			continue
		}
		sum := sha256.Sum256(b)
		exp, listed := want[name]
		if !listed {
			t.Errorf("%s is embedded but not listed in SHA256SUMS", name)
			continue
		}
		if got := hex.EncodeToString(sum[:]); got != exp {
			t.Errorf("%s: sha256 %s, SHA256SUMS says %s", name, got, exp)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("%s is listed in SHA256SUMS but not embedded", name)
	}
}

func TestNodeEnvIsMinimal(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "--require /tmp/evil.js")
	t.Setenv("NODE_PATH", "/tmp/evil")
	t.Setenv("SWEET_COOKIE_CHROME_SAFE_STORAGE_PASSWORD", "x")
	t.Setenv("SWEET_COOKIE_BROWSERS", "firefox")
	t.Setenv("npm_config_registry", "https://evil.example")
	t.Setenv("PATH", "/usr/local/bin:.:bin::/usr/bin")
	t.Setenv("SWEET_COOKIE_LINUX_KEYRING", "gnome")
	t.Setenv("HOME", "/home/someone")

	env := nodeEnv("/private/tmp", "/private/out.json")
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for _, k := range []string{"NODE_OPTIONS", "NODE_PATH", "SWEET_COOKIE_CHROME_SAFE_STORAGE_PASSWORD", "SWEET_COOKIE_BROWSERS", "npm_config_registry"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s leaked into the node environment", k)
		}
	}
	if runtime.GOOS != "windows" && got["PATH"] != "/usr/local/bin:/usr/bin" {
		t.Errorf("PATH = %q, want the absolute entries only", got["PATH"])
	}
	if got["SWEET_COOKIE_LINUX_KEYRING"] != "gnome" {
		t.Error("the keyring backend selector must reach node")
	}
	if got["HOME"] != "/home/someone" || got["TMPDIR"] != "/private/tmp" || got["BOURSOBANK_OUTPUT_PATH"] != "/private/out.json" {
		t.Errorf("required variables missing: %v", got)
	}
}

// v10 = Chromium's Linux scheme without keyring: AES-128-CBC, key =
// PBKDF2-SHA1("peanuts", "saltysalt", 1 iteration), IV = 16 spaces.
func encryptV10(t *testing.T, plain string) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, "peanuts", []byte("saltysalt"), 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append([]byte(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	cipher.NewCBCEncrypter(blk, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(buf, buf)
	return append([]byte("v10"), buf...)
}

// End to end against a FAKE Chrome profile (never the real one, never the
// real keyring): the embedded helper decrypts only BoursoBank cookies, the
// auto-pick returns a profile path, and nothing is left in TMPDIR.
func TestExtractCookiesFakeProfile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fake v10 profile is the Linux cookie scheme")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	home := t.TempDir()
	goTmp := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TMPDIR", goTmp)
	// Keep secret-tool away from the real session keyring.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(home, "no-bus"))
	t.Setenv("XDG_RUNTIME_DIR", home)

	profileDir := filepath.Join(home, ".config", "google-chrome", "Default")
	if err := os.MkdirAll(filepath.Join(profileDir, "Network"), 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(profileDir, "Network", "Cookies")
	expires := (time.Now().Add(24*time.Hour).Unix() + 11644473600) * 1_000_000 // Chrome epoch, µs
	rows := []struct{ host, name, value string }{
		{".boursobank.com", "brsxds_x", "bank-secret"},
		{"clients.boursobank.com", "PHPSESSID", "sess"},
		{".boursorama.com", "bourse", "bourse-secret"},
		{".example.com", "other", "must-not-leak"},
	}
	var sql strings.Builder
	sql.WriteString(`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT);
INSERT INTO meta VALUES('version','23');
CREATE TABLE cookies(creation_utc INTEGER, host_key TEXT, name TEXT, value TEXT, path TEXT,
  expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, last_access_utc INTEGER,
  has_expires INTEGER, is_persistent INTEGER, priority INTEGER, encrypted_value BLOB,
  samesite INTEGER, source_scheme INTEGER, source_port INTEGER, last_update_utc INTEGER);
`)
	for _, r := range rows {
		fmt.Fprintf(&sql, "INSERT INTO cookies(host_key,name,value,path,expires_utc,is_secure,is_httponly,encrypted_value,samesite,last_update_utc) VALUES('%s','%s','','/',%d,1,1,X'%s',0,%d);\n",
			r.host, r.name, expires, hex.EncodeToString(encryptV10(t, r.value)), expires)
	}
	//nolint:gosec // G204: node from LookPath, fixed script; builds a test fixture
	mk := exec.Command(nodePath, "--input-type=module", "-e",
		`import {DatabaseSync} from 'node:sqlite'; const db = new DatabaseSync(process.argv[1]); db.exec(process.argv[2]); db.close();`,
		dbPath, sql.String())
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("cannot build the fake Cookies DB with this node (%v): %s", err, out)
	}

	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ex, err := ExtractCookies(ctx, "", &log)
	if err != nil {
		if strings.Contains(err.Error(), "trop ancien") {
			t.Skip(err)
		}
		t.Fatalf("ExtractCookies: %v\nlog:\n%s", err, log.String())
	}
	if ex.Profile != profileDir {
		t.Errorf("auto-picked profile = %q, want %q", ex.Profile, profileDir)
	}
	bank := ex.CookiesByHost["clients.boursobank.com"]
	if !strings.Contains(bank, "brsxds_x=bank-secret") || !strings.Contains(bank, "PHPSESSID=sess") {
		t.Errorf("boursobank jar = %q", bank)
	}
	if b := ex.CookiesByHost["clients.boursorama.com"]; !strings.Contains(b, "bourse=bourse-secret") {
		t.Errorf("boursorama jar = %q", b)
	}
	if all := MergedHeader(ex.CookiesByHost); strings.Contains(all, "must-not-leak") {
		t.Errorf("a non-BoursoBank cookie was extracted: %q", all)
	}
	if strings.Contains(log.String(), "ExperimentalWarning") {
		t.Errorf("node warning noise on stderr: %s", log.String())
	}
	for _, v := range []string{"bank-secret", "bourse-secret", "must-not-leak"} {
		if strings.Contains(log.String(), v) {
			t.Errorf("cookie value %q reached the log: %s", v, log.String())
		}
	}
	left, err := os.ReadDir(goTmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}
