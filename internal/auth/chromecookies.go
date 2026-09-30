// Package auth: dual-domain cookie extraction from the local Chrome profile
// (no manual paste) + dashboard bearer bootstrap. Secrets never logged.
//
// The decryption code is the vendored sweet-cookie (./sweetcookie, embedded
// in the binary): no npm, no registry, no cache folder. Each extraction runs
// node in a fresh private folder with a minimal environment, and deletes that
// folder when node exits — also when node is killed on timeout.
package auth

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed load.mjs
var loadScript []byte

//go:embed sweetcookie
var sweetCookie embed.FS

// The two registrable domains BoursoBank spans. Banking works with the
// boursobank jar alone; securities/ORD/bourse REQUIRE the boursorama jar too
// (x-domain-authentification SSO sets it). Always extract+merge both.
var cookieTargets = []struct{ host, url string }{
	{"clients.boursobank.com", "https://clients.boursobank.com/"},
	{"clients.boursorama.com", "https://clients.boursorama.com/"},
}

// node:sqlite loads without a flag from Node 22.13.
const minNodeMajor, minNodeMinor = 22, 13

const runTimeout = 20 * time.Second

type scriptIn struct {
	TargetURL     string `json:"target_url"`
	ChromeProfile string `json:"chrome_profile"`
	TimeoutMillis int    `json:"timeout_millis"`
}
type scriptOut struct {
	CookieHeader string `json:"cookie_header"`
	CookieCount  int    `json:"cookie_count"`
	Profile      string `json:"profile"`
	Error        string `json:"error"`
}

// Extracted is the result of one extraction.
type Extracted struct {
	// CookiesByHost maps "clients.boursobank.com" / "clients.boursorama.com"
	// to a Cookie header value. Held in memory only, never written to disk.
	CookiesByHost map[string]string
	// Profile is the Chrome profile the cookies came from: the one asked
	// for, or the one the auto-pick chose (a directory path). The caller
	// pins it so the all-profile scan runs once.
	Profile string
}

// ExtractCookies runs the embedded node helper once per domain. Requires
// Node ≥22.13 on PATH.
func ExtractCookies(ctx context.Context, chromeProfile string, log io.Writer) (Extracted, error) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return Extracted{}, fmt.Errorf("node introuvable (requis pour l’extraction des cookies Chrome ; installer Node ≥%d.%d)", minNodeMajor, minNodeMinor)
	}
	runDir, err := os.MkdirTemp("", "boursocli-auth-")
	if err != nil {
		return Extracted{}, err
	}
	defer func() { _ = os.RemoveAll(runDir) }()
	if err := materialize(runDir); err != nil {
		return Extracted{}, fmt.Errorf("préparation du dossier d’extraction : %w", err)
	}
	if err := checkNodeVersion(ctx, nodePath, runDir); err != nil {
		return Extracted{}, err
	}

	res := Extracted{CookiesByHost: make(map[string]string, len(cookieTargets)), Profile: chromeProfile}
	for _, t := range cookieTargets {
		o, err := runOne(ctx, nodePath, runDir, res.Profile, t.url, log)
		if err != nil {
			// boursorama jar may be absent if the user never visited bourse;
			// boursobank is mandatory.
			if t.host == "clients.boursobank.com" {
				return Extracted{}, fmt.Errorf("échec de l’extraction des cookies pour %s : %w (Chrome est-il connecté à BoursoBank ?)", t.host, err)
			}
			_, _ = fmt.Fprintf(orDiscard(log), "avert : aucun cookie pour %s (%v) — titres/ORD indisponibles tant que vous n’aurez pas visité l’espace bourse dans Chrome\n", t.host, err)
			continue
		}
		// Both jars must come from the same profile: the first call may
		// auto-pick one, the second reuses it.
		if res.Profile == "" {
			res.Profile = o.Profile
		}
		if o.CookieHeader != "" {
			res.CookiesByHost[t.host] = o.CookieHeader
		}
	}
	if res.CookiesByHost["clients.boursobank.com"] == "" {
		return Extracted{}, fmt.Errorf("aucun cookie de session BoursoBank dans le profil Chrome %q — se connecter d’abord à clients.boursobank.com dans Chrome", chromeProfile)
	}
	return res, nil
}

// MergedHeader concatenates both jars for a request to any *.boursobank.com /
// *.boursorama.com host (cookies are domain-scoped; sending both is harmless
// and required for the bourse plane).
func MergedHeader(byHost map[string]string) string {
	var buf bytes.Buffer
	for _, t := range cookieTargets {
		if v := byHost[t.host]; v != "" {
			if buf.Len() > 0 {
				buf.WriteString("; ")
			}
			buf.WriteString(v)
		}
	}
	return buf.String()
}

// materialize writes load.mjs and the vendored sweet-cookie into runDir
// (owner-only). load.mjs imports it by relative path: no node_modules.
//
//	runDir/package.json          {"type":"module"}
//	runDir/load.mjs
//	runDir/sweet-cookie/...      vendored files
//	runDir/tmp/                  TMPDIR for node (DB snapshots)
func materialize(runDir string) error {
	if err := os.WriteFile(filepath.Join(runDir, "package.json"), []byte(`{"private":true,"type":"module"}`+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runDir, "load.mjs"), loadScript, 0o600); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(runDir, "tmp"), 0o700); err != nil {
		return err
	}
	dst := filepath.Join(runDir, "sweet-cookie")
	return fs.WalkDir(sweetCookie, "sweetcookie", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "sweetcookie")
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := sweetCookie.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}

var reNodeVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.`)

func checkNodeVersion(ctx context.Context, nodePath, runDir string) error {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, nodePath, "--version")
	cmd.Dir = runDir
	cmd.Env = nodeEnv(filepath.Join(runDir, "tmp"), "")
	b, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("node --version : %w", err)
	}
	v := strings.TrimSpace(string(b))
	m := reNodeVersion.FindStringSubmatch(v)
	if m == nil {
		return fmt.Errorf("version de node illisible : %q", v)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minNodeMajor || (major == minNodeMajor && minor < minNodeMinor) {
		return fmt.Errorf("node %s trop ancien : Node ≥%d.%d requis (node:sqlite sans flag)", v, minNodeMajor, minNodeMinor)
	}
	return nil
}

func runOne(ctx context.Context, nodePath, runDir, profile, targetURL string, log io.Writer) (scriptOut, error) {
	outPath := filepath.Join(runDir, "out.json")
	_ = os.Remove(outPath) // never read the previous target's result
	in, err := json.Marshal(scriptIn{TargetURL: targetURL, ChromeProfile: profile, TimeoutMillis: 8000})
	if err != nil {
		return scriptOut{}, err
	}
	c, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	// --no-addons: sweet-cookie needs no native module, so none may load.
	// --disallow-code-generation-from-strings: no eval / new Function.
	//nolint:gosec // nodePath from LookPath; the script is our own embedded load.mjs in a private MkdirTemp dir
	cmd := exec.CommandContext(c, nodePath, "--no-addons", "--disallow-code-generation-from-strings", filepath.Join(runDir, "load.mjs"))
	cmd.Dir = runDir
	cmd.Env = nodeEnv(filepath.Join(runDir, "tmp"), outPath)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout = io.Discard
	cmd.Stderr = orDiscard(log)
	// A keyring helper (secret-tool…) killed with node may keep stderr open:
	// do not wait for it forever.
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	b, readErr := os.ReadFile(outPath) //nolint:gosec // outPath is our own file in a private os.MkdirTemp dir
	if readErr != nil {
		if runErr != nil {
			return scriptOut{}, runErr
		}
		return scriptOut{}, readErr
	}
	var o scriptOut
	if err := json.Unmarshal(b, &o); err != nil {
		return scriptOut{}, err
	}
	if o.Error != "" {
		return scriptOut{}, fmt.Errorf("%s", o.Error)
	}
	if strings.ContainsAny(o.CookieHeader, "\r\n") {
		return scriptOut{}, fmt.Errorf("en-tête Cookie invalide (retour à la ligne)")
	}
	return o, nil
}

// nodeEnv is the WHOLE environment node sees; nothing else is inherited.
// NODE_OPTIONS / NODE_PATH could load other code, and most SWEET_COOKIE_*
// variables change what sweet-cookie reads (browsers, profile, even a fixed
// "Safe Storage" password). Kept: what the keyring helpers need, the
// keyring backend selector, and PATH without relative entries (node's cwd is
// our private folder; secret-tool may live in /run/current-system/sw/bin on
// NixOS or in a Homebrew prefix).
func nodeEnv(tmpDir, outPath string) []string {
	keep := []string{
		"HOME", "USER", "LOGNAME", "LANG", "LC_ALL",
		// Linux keyring access (libsecret over D-Bus, or KWallet) and the
		// Chrome profile location.
		"XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CURRENT_DESKTOP",
		"DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY",
		"KDE_FULL_SESSION", "KDE_SESSION_VERSION",
		// gnome | kwallet | basic: for a Chrome whose keyring differs from
		// the desktop (e.g. --password-store=gnome-libsecret under KDE).
		"SWEET_COOKIE_LINUX_KEYRING",
	}
	if runtime.GOOS == "windows" {
		keep = append(keep, "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC",
			"USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATHEXT")
	}
	env := make([]string, 0, len(keep)+5)
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "PATH="+absolutePath(os.Getenv("PATH")))
	env = append(env, "TMPDIR="+tmpDir, "TMP="+tmpDir, "TEMP="+tmpDir)
	if outPath != "" {
		env = append(env, "BOURSOBANK_OUTPUT_PATH="+outPath)
	}
	return env
}

// absolutePath drops empty and relative PATH entries ("", ".", "bin"…):
// they would resolve against node's working folder.
func absolutePath(p string) string {
	var keep []string
	for _, d := range filepath.SplitList(p) {
		if filepath.IsAbs(d) {
			keep = append(keep, d)
		}
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// vendoredFiles lists the embedded sweet-cookie files (slash paths relative
// to the sweetcookie folder). Used by the integrity test.
func vendoredFiles() (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(sweetCookie, "sweetcookie", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := sweetCookie.ReadFile(p)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(p, "sweetcookie/")] = b
		return nil
	})
	return files, err
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
