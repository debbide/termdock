package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"webterm-cf/internal/app"
	"webterm-cf/internal/config"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}

	flags := flag.NewFlagSet("webterm", flag.ExitOnError)
	configPath := flags.String("config", "", "configuration file")
	listen := flags.String("listen", "", "HTTP listen address")
	_ = flags.Parse(os.Args[1:])

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Server.Listen = *listen
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := app.Run(ctx, cfg); err != nil {
		slog.Error("webterm stopped", "error", err)
		os.Exit(1)
	}
}
