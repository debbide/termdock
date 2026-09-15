package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsAreSafe(t *testing.T) {
	cfg := Defaults()
	if cfg.Server.Listen != "127.0.0.1:7681" || cfg.Terminal.MaxSessions != 1 || !cfg.Security.CookieSecure {
		t.Fatal("unsafe defaults")
	}
	if cfg.Terminal.SessionRetention != time.Hour {
		t.Fatalf("unexpected session retention %v", cfg.Terminal.SessionRetention)
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

// writeConfig stores a configuration file with the group/other write bits
// cleared, which Load requires.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOmittedCookieSecureKeepsSecureDefault(t *testing.T) {
	path := writeConfig(t, `{"server":{"listen":"127.0.0.1:9000"}}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.CookieSecure {
		t.Fatal("omitting cookie_secure disabled the secure default")
	}
	if cfg.Server.Listen != "127.0.0.1:9000" {
		t.Fatalf("unexpected listen address %q", cfg.Server.Listen)
	}
}

func TestExplicitCookieSecureIsHonored(t *testing.T) {
	for _, testCase := range []struct {
		body string
		want bool
	}{
		{body: `{"security":{"cookie_secure":false}}`, want: false},
		{body: `{"security":{"cookie_secure":true}}`, want: true},
	} {
		cfg, err := Load(writeConfig(t, testCase.body))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Security.CookieSecure != testCase.want {
			t.Fatalf("cookie_secure = %v, want %v", cfg.Security.CookieSecure, testCase.want)
		}
	}
}

func TestLoadRejectsWorldWritableConfig(t *testing.T) {
	path := writeConfig(t, `{}`)
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("world-writable configuration was accepted")
	}
}

func TestTrustedProxiesValidation(t *testing.T) {
	for _, body := range []string{
		`{"security":{"trusted_proxies":["10.0.0.0/8","198.51.100.7","::1"]}}`,
		`{"security":{"trusted_proxies":[]}}`,
	} {
		if _, err := Load(writeConfig(t, body)); err != nil {
			t.Fatalf("valid trusted_proxies rejected: %v", err)
		}
	}
	for _, body := range []string{
		`{"security":{"trusted_proxies":["not-an-address"]}}`,
		`{"security":{"trusted_proxies":[""]}}`,
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("invalid trusted_proxies accepted: %s", body)
		}
	}
}
