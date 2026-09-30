// Package config persists CLI state (versioned JSON, OS config dir, --config override).
//
// The only secret on disk is the bearer (≤24h JWT) and the user hash. The
// Chrome cookie jars — including the long-lived "rememberme" device-trust
// cookie — are never written: they are read from Chrome when a command needs
// them, and live in memory only. `config show` redacts; `config wipe`
// removes the bearer. Never logged.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const Version = 2

// Config is the on-disk state.
type Config struct {
	Version       int    `json:"version"`
	ChromeProfile string `json:"chrome_profile,omitempty"`  // "Default", "Profile 1", or a path
	Bearer        string `json:"bearer,omitempty"`          // scraped DEFAULT_API_BEARER (24h)
	BearerSavedAt string `json:"bearer_saved_at,omitempty"` // RFC3339 UTC: when bearer was last established
	BearerExp     string `json:"bearer_exp,omitempty"`      // RFC3339 UTC: JWT exp claim — steipete ExpiresAt pattern
	UserHash      string `json:"user_hash,omitempty"`       // scraped USER_HASH
	HTTPUserAgent string `json:"http_user_agent,omitempty"`
	// AllowSessionRefresh lets the CLI send POST session/auth/refresh, the
	// one request that changes state at the bank (it extends the session).
	// Off by default: when the bearer dies, a new one comes from the live
	// Chrome session, and the session lifetime stays the one Chrome gives.
	AllowSessionRefresh bool `json:"allow_session_refresh,omitempty"`
}

// Path returns the config file path: --config override, else $XDG/OS config dir.
func Path(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "boursocli", "config.json"), nil
}

// Load reads the config. The file and its folder must be private to the
// current user (see checkPrivate); a config written by an older version with
// cookie jars inside is rewritten at once without them.
func Load(path string) (*Config, error) {
	if err := checkPrivateDir(filepath.Dir(path)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := checkPrivateFile(path); err != nil {
		if os.IsNotExist(err) {
			return &Config{Version: Version}, nil
		}
		return nil, err
	}
	b, err := os.ReadFile(path) //nolint:gosec // path is our own config file (OS config dir or the user's explicit --config), checked private above
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	var legacy struct {
		CookiesByHost map[string]string `json:"cookies_by_host"`
	}
	if json.Unmarshal(b, &legacy) == nil && legacy.CookiesByHost != nil {
		// v1 stored the full Chrome cookie jars. Drop them from disk now,
		// not at the next Save (which may never come).
		if err := c.Save(path); err != nil {
			return nil, fmt.Errorf("suppression des cookies de l’ancienne config %s : %w", path, err)
		}
	}
	return &c, nil
}

// Save writes the config atomically. The temp file is created with a random
// name, O_EXCL and mode 0600 in the (private) target folder, so it can be
// neither pre-planted nor a symlink; a crash or a concurrent run mid-write
// never corrupts or truncates the file.
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	// 0700: this directory holds the session-secret config.json — owner-only.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	c.Version = Version
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, ".config-*.tmp") // O_EXCL, 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ForgetSession removes the bearer and user hash (`config wipe`). The
// non-secret settings stay.
func (c *Config) ForgetSession() {
	c.Bearer, c.BearerSavedAt, c.BearerExp, c.UserHash = "", "", "", ""
}

// BearerLikelyExpired reports whether the stored bearer JWT is expired or will
// expire within margin. Mirrors steipete ordercli TokenLikelyExpired pattern.
// When true the caller should re-bootstrap (re-GET dashboard) from the live
// Chrome session.
func (c *Config) BearerLikelyExpired(margin time.Duration) bool {
	if c.Bearer == "" {
		return true
	}
	if c.BearerExp == "" {
		if c.BearerSavedAt == "" {
			return true
		}
		t, err := time.Parse(time.RFC3339, c.BearerSavedAt)
		if err != nil {
			return true
		}
		return time.Since(t) > 23*time.Hour
	}
	exp, err := time.Parse(time.RFC3339, c.BearerExp)
	if err != nil {
		return true
	}
	return !exp.After(time.Now().Add(margin))
}

// Redacted returns a copy safe to print (`config show`).
func (c *Config) Redacted() map[string]any {
	red := func(s string) string {
		if s == "" {
			return ""
		}
		return "*** (" + itoa(len(s)) + " chars)"
	}
	return map[string]any{
		"version":               c.Version,
		"chrome_profile":        c.ChromeProfile,
		"bearer":                red(c.Bearer),
		"bearer_saved_at":       c.BearerSavedAt, // a timestamp, not a secret
		"bearer_exp":            c.BearerExp,     // JWT exp — the 24h ceiling
		"user_hash":             red(c.UserHash), // account-linkable → redact (show nothing identifying)
		"http_user_agent":       c.HTTPUserAgent,
		"allow_session_refresh": c.AllowSessionRefresh,
		"cookies_on_disk":       false, // by design: read from Chrome, memory only
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
