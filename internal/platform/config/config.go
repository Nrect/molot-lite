// Package config loads and validates platform configuration from
// environment variables. Load is fail-fast: every missing or invalid
// variable is reported in a single error, not one at a time.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// LogFormat selects the slog handler. Closed enum.
type LogFormat string

const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// minJWTSecretBytes is the floor for HS256 keys: RFC 7518 §3.2 requires
// a key at least as long as the hash output (SHA-256 = 32 bytes).
const minJWTSecretBytes = 32

// Config is the full, typed runtime configuration of the service.
type Config struct {
	HTTPPort    int       // HTTP_PORT (default 8080)
	DatabaseURL string    // DATABASE_URL (required)
	JWTSecret   string    // JWT_SECRET (required, at least 32 bytes)
	LogFormat   LogFormat // LOG_FORMAT (default json)
	BcryptCost  int       // BCRYPT_COST (default 10, bcrypt.MinCost..bcrypt.MaxCost)

	// CORSOrigins is the cross-origin allowlist (CORS_ORIGINS,
	// comma-separated). Empty keeps CORS disabled — the safe default:
	// no origin is ever reflected unless explicitly listed.
	CORSOrigins []string

	RateLimitRPS   int // RATE_LIMIT_RPS (default 2, 0 disables the limiter)
	RateLimitBurst int // RATE_LIMIT_BURST (default 5, at least 1)

	// Telegram credentials for outbid notifications; optional, but set
	// both or neither. Empty means notifications fall back to the log.
	TelegramBotToken string // TELEGRAM_BOT_TOKEN
	TelegramChatID   string // TELEGRAM_CHAT_ID
}

// Load reads the configuration from the process environment.
// On failure the returned error enumerates ALL problems at once.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	l := &loader{lookup: lookup}

	cfg := Config{
		HTTPPort:    l.port("HTTP_PORT", 8080),
		DatabaseURL: l.required("DATABASE_URL"),
		JWTSecret:   l.required("JWT_SECRET"),
		LogFormat:   LogFormat(l.enum("LOG_FORMAT", string(LogFormatJSON), string(LogFormatJSON), string(LogFormatText))),
		BcryptCost:  l.intInRange("BCRYPT_COST", bcrypt.DefaultCost, bcrypt.MinCost, bcrypt.MaxCost),

		CORSOrigins:    l.csv("CORS_ORIGINS"),
		RateLimitRPS:   l.intInRange("RATE_LIMIT_RPS", 2, 0, 10_000),
		RateLimitBurst: l.intInRange("RATE_LIMIT_BURST", 5, 1, 10_000),

		TelegramBotToken: l.optional("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   l.optional("TELEGRAM_CHAT_ID"),
	}

	if cfg.JWTSecret != "" && len(cfg.JWTSecret) < minJWTSecretBytes {
		l.fail("JWT_SECRET", fmt.Sprintf("must be at least %d bytes, got %d", minJWTSecretBytes, len(cfg.JWTSecret)))
	}

	if (cfg.TelegramBotToken == "") != (cfg.TelegramChatID == "") {
		l.fail("TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID", "must be set together: both or neither")
	}

	if len(l.errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", errors.Join(l.errs...))
	}
	return cfg, nil
}

// loader accumulates every problem instead of failing on the first one.
type loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (l *loader) fail(key, reason string) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, reason))
}

func (l *loader) optional(key string) string {
	v, _ := l.lookup(key)
	return v
}

// csv splits a comma-separated value into trimmed, non-empty items;
// an unset or empty variable yields nil.
func (l *loader) csv(key string) []string {
	raw, ok := l.lookup(key)
	if !ok || raw == "" {
		return nil
	}
	var items []string
	for _, part := range strings.Split(raw, ",") {
		if item := strings.TrimSpace(part); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func (l *loader) required(key string) string {
	v, ok := l.lookup(key)
	if !ok || v == "" {
		l.fail(key, "required environment variable is not set")
		return ""
	}
	return v
}

func (l *loader) enum(key, def string, allowed ...string) string {
	raw, ok := l.lookup(key)
	if !ok || raw == "" {
		return def
	}
	for _, a := range allowed {
		if raw == a {
			return raw
		}
	}
	l.fail(key, fmt.Sprintf("must be one of %v, got %q", allowed, raw))
	return def
}

func (l *loader) intInRange(key string, def, minVal, maxVal int) int {
	raw, ok := l.lookup(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < minVal || v > maxVal {
		l.fail(key, fmt.Sprintf("must be an integer in [%d, %d], got %q", minVal, maxVal, raw))
		return def
	}
	return v
}

func (l *loader) port(key string, def int) int {
	raw, ok := l.lookup(key)
	if !ok || raw == "" {
		return def
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p < 1 || p > 65535 {
		l.fail(key, fmt.Sprintf("must be a TCP port (1-65535), got %q", raw))
		return def
	}
	return p
}
