package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseQuickURL(t *testing.T) {
	line := `INF Your quick Tunnel has been created! Visit https://calm-river.trycloudflare.com`
	if value := ParseQuickURL(line); value != "https://calm-river.trycloudflare.com" {
		t.Fatalf("unexpected URL %q", value)
	}
	if value := ParseQuickURL("https://example.com"); value != "" {
		t.Fatalf("accepted unexpected URL %q", value)
	}
}

func TestFixedTokenPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments, err := commandArguments("fixed", "", path)
	if err != nil {
		t.Fatal(err)
	}
	if arguments[len(arguments)-1] != "secret" {
		t.Fatal("token was not loaded")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := commandArguments("fixed", "", path); err == nil {
		t.Fatal("insecure token permissions accepted")
	}
}

func TestFixedTunnelRestartsAfterExit(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "cloudflared")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager := New()
	if _, err := manager.Start(ctx, binary, "fixed", "127.0.0.1:7681", tokenFile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = manager.Stop()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if manager.Status().RestartCount > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("fixed tunnel was not restarted")
}

func TestNextRestartDelay(t *testing.T) {
	if got := nextRestartDelay(initialRestartDelay); got != 2*initialRestartDelay {
		t.Fatalf("expected delay to double, got %v", got)
	}
	if got := nextRestartDelay(maxRestartDelay); got != maxRestartDelay {
		t.Fatalf("expected delay to cap at %v, got %v", maxRestartDelay, got)
	}
	if got := nextRestartDelay(4 * time.Minute); got != maxRestartDelay {
		t.Fatalf("expected delay to cap at %v, got %v", maxRestartDelay, got)
	}
}
