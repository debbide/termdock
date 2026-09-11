package config

import "testing"

func TestDefaultsAreSafe(t *testing.T) {
	cfg := Defaults()
	if cfg.Server.Listen != "127.0.0.1:7681" || cfg.Terminal.MaxSessions != 1 || !cfg.Security.CookieSecure {
		t.Fatal("unsafe defaults")
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}
