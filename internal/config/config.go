package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Server     Server     `json:"server"`
	Terminal   Terminal   `json:"terminal"`
	Security   Security   `json:"security"`
	Cloudflare Cloudflare `json:"cloudflare"`
}

type Cloudflare struct {
	Mode      string `json:"mode"`
	Binary    string `json:"binary"`
	TokenFile string `json:"token_file"`
}

type Server struct {
	Listen string `json:"listen"`
}

type Terminal struct {
	Shell       string        `json:"shell"`
	WorkingDir  string        `json:"working_directory"`
	MaxSessions int           `json:"max_sessions"`
	IdleTimeout time.Duration `json:"-"`
	MaxLifetime time.Duration `json:"-"`
}

type Security struct {
	TrustedOrigins []string `json:"trusted_origins"`
	CookieSecure   bool     `json:"cookie_secure"`
	LoginRateLimit int      `json:"login_rate_limit"`
	MaxMessageSize int64    `json:"max_message_size"`
}

type diskConfig struct {
	Server   Server `json:"server"`
	Terminal struct {
		Shell       string `json:"shell"`
		WorkingDir  string `json:"working_directory"`
		MaxSessions int    `json:"max_sessions"`
		IdleTimeout string `json:"idle_timeout"`
		MaxLifetime string `json:"max_lifetime"`
	} `json:"terminal"`
	Security   Security   `json:"security"`
	Cloudflare Cloudflare `json:"cloudflare"`
}

func Defaults() Config {
	return Config{
		Server:     Server{Listen: "127.0.0.1:7681"},
		Terminal:   Terminal{Shell: defaultShell(), WorkingDir: defaultWorkingDir(), MaxSessions: 1, IdleTimeout: 15 * time.Minute, MaxLifetime: time.Hour},
		Security:   Security{CookieSecure: true, LoginRateLimit: 5, MaxMessageSize: 64 << 10},
		Cloudflare: Cloudflare{Mode: "disabled", Binary: "/usr/local/bin/cloudflared", TokenFile: "/etc/webterm/cloudflare-token"},
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return Config{}, err
		}
		if info.Mode().Perm()&0o022 != 0 {
			return Config{}, errors.New("configuration file must not be writable by group or others")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		var disk diskConfig
		if err := json.Unmarshal(data, &disk); err != nil {
			return Config{}, fmt.Errorf("decode configuration: %w", err)
		}
		merge(&cfg, disk)
	}
	applyEnvironment(&cfg)
	return cfg, Validate(cfg)
}

func Validate(cfg Config) error {
	if cfg.Server.Listen == "" {
		return errors.New("listen address is required")
	}
	if !filepath.IsAbs(cfg.Terminal.Shell) {
		return errors.New("terminal shell must be an absolute path")
	}
	info, err := os.Stat(cfg.Terminal.Shell)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return errors.New("terminal shell must be an executable file")
	}
	if cfg.Terminal.MaxSessions < 1 || cfg.Security.LoginRateLimit < 1 || cfg.Security.MaxMessageSize < 1024 {
		return errors.New("session, rate, and message limits must be positive")
	}
	if cfg.Terminal.IdleTimeout <= 0 || cfg.Terminal.MaxLifetime <= 0 {
		return errors.New("timeouts must be positive")
	}
	if cfg.Cloudflare.Mode != "disabled" && cfg.Cloudflare.Mode != "quick" && cfg.Cloudflare.Mode != "fixed" {
		return errors.New("cloudflare mode must be disabled, quick, or fixed")
	}
	if cfg.Cloudflare.Mode != "disabled" && !filepath.IsAbs(cfg.Cloudflare.Binary) {
		return errors.New("cloudflared binary must be an absolute path")
	}
	if cfg.Cloudflare.Mode == "fixed" && !filepath.IsAbs(cfg.Cloudflare.TokenFile) {
		return errors.New("cloudflare token file must be an absolute path")
	}
	return nil
}

func merge(cfg *Config, disk diskConfig) {
	if disk.Server.Listen != "" {
		cfg.Server.Listen = disk.Server.Listen
	}
	if disk.Terminal.Shell != "" {
		cfg.Terminal.Shell = disk.Terminal.Shell
	}
	if disk.Terminal.WorkingDir != "" {
		cfg.Terminal.WorkingDir = disk.Terminal.WorkingDir
	}
	if disk.Terminal.MaxSessions > 0 {
		cfg.Terminal.MaxSessions = disk.Terminal.MaxSessions
	}
	if value, err := time.ParseDuration(disk.Terminal.IdleTimeout); err == nil && value > 0 {
		cfg.Terminal.IdleTimeout = value
	}
	if value, err := time.ParseDuration(disk.Terminal.MaxLifetime); err == nil && value > 0 {
		cfg.Terminal.MaxLifetime = value
	}
	if disk.Security.LoginRateLimit > 0 {
		cfg.Security.LoginRateLimit = disk.Security.LoginRateLimit
	}
	if disk.Security.MaxMessageSize > 0 {
		cfg.Security.MaxMessageSize = disk.Security.MaxMessageSize
	}
	if disk.Security.TrustedOrigins != nil {
		cfg.Security.TrustedOrigins = disk.Security.TrustedOrigins
	}
	cfg.Security.CookieSecure = disk.Security.CookieSecure
	if disk.Cloudflare.Mode != "" {
		cfg.Cloudflare.Mode = disk.Cloudflare.Mode
	}
	if disk.Cloudflare.Binary != "" {
		cfg.Cloudflare.Binary = disk.Cloudflare.Binary
	}
	if disk.Cloudflare.TokenFile != "" {
		cfg.Cloudflare.TokenFile = disk.Cloudflare.TokenFile
	}
}

func applyEnvironment(cfg *Config) {
	if value := os.Getenv("WEBTERM_LISTEN"); value != "" {
		cfg.Server.Listen = value
	}
	if value := os.Getenv("WEBTERM_SHELL"); value != "" {
		cfg.Terminal.Shell = value
	}
	if value := os.Getenv("WEBTERM_WORKING_DIRECTORY"); value != "" {
		cfg.Terminal.WorkingDir = value
	}
	if value, err := strconv.Atoi(os.Getenv("WEBTERM_MAX_SESSIONS")); err == nil && value > 0 {
		cfg.Terminal.MaxSessions = value
	}
	if value, err := time.ParseDuration(os.Getenv("WEBTERM_IDLE_TIMEOUT")); err == nil && value > 0 {
		cfg.Terminal.IdleTimeout = value
	}
	if value, err := time.ParseDuration(os.Getenv("WEBTERM_MAX_LIFETIME")); err == nil && value > 0 {
		cfg.Terminal.MaxLifetime = value
	}
	if value := os.Getenv("WEBTERM_TUNNEL_MODE"); value != "" {
		cfg.Cloudflare.Mode = value
	}
	if value := os.Getenv("WEBTERM_CLOUDFLARED"); value != "" {
		cfg.Cloudflare.Binary = value
	}
	if value := os.Getenv("WEBTERM_CLOUDFLARE_TOKEN_FILE"); value != "" {
		cfg.Cloudflare.TokenFile = value
	}
}

func defaultShell() string {
	for _, path := range []string{"/bin/bash", "/bin/sh"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path
		}
	}
	return "/bin/sh"
}

func defaultWorkingDir() string {
	if current, err := user.Current(); err == nil && current.HomeDir != "" {
		return current.HomeDir
	}
	return "/tmp"
}
