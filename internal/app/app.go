package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"webterm-cf/internal/auth"
	"webterm-cf/internal/config"
	"webterm-cf/internal/server"
	"webterm-cf/internal/tunnel"
)

//go:embed static/*
var static embed.FS

func Run(ctx context.Context, cfg config.Config) error {
	var manager *auth.Manager
	var token string
	var err error
	authLifetime := cfg.Terminal.MaxLifetime
	if authLifetime <= 0 {
		authLifetime = 30 * 24 * time.Hour
	}
	fixedToken := os.Getenv("WEBTERM_ACCESS_TOKEN")
	if fixedToken != "" {
		manager, token, err = auth.NewWithToken(authLifetime, fixedToken)
	} else {
		manager, token, err = auth.New(authLifetime)
	}
	if err != nil {
		return err
	}
	assets, err := fs.Sub(static, "static")
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           server.New(cfg, manager, assets).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	tunnelManager := tunnel.New()
	publicURL, err := tunnelManager.Start(ctx, cfg.Cloudflare.Binary, cfg.Cloudflare.Mode, cfg.Server.Listen, cfg.Cloudflare.TokenFile)
	if err != nil {
		return fmt.Errorf("start cloudflare tunnel: %w", err)
	}
	defer tunnelManager.Stop()

	slog.Info("webterm starting", "listen", cfg.Server.Listen)
	if publicURL != "" {
		fmt.Printf("Public URL: %s\n", publicURL)
	}
	// A configured token is a long-lived secret. Printing it would leak it into
	// the service journal, so only a freshly generated one-time token is shown.
	if fixedToken == "" {
		fmt.Printf("One-time token: %s\n", token)
	} else {
		fmt.Println("Access token: read from WEBTERM_ACCESS_TOKEN")
	}
	fmt.Printf("File manager root: %s\n", cfg.Terminal.WorkingDir)
	errChannel := make(chan error, 1)
	go func() { errChannel <- httpServer.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
