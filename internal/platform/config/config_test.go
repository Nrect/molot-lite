package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://localhost:5432/app",
		"JWT_SECRET":   strings.Repeat("s", 32),
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := load(lookupFrom(validEnv()))
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.HTTPPort)
	assert.Equal(t, LogFormatJSON, cfg.LogFormat)
	assert.Equal(t, 10, cfg.BcryptCost)
	assert.Nil(t, cfg.CORSOrigins, "CORS is disabled unless origins are configured")
	assert.Equal(t, 2, cfg.RateLimitRPS)
	assert.Equal(t, 5, cfg.RateLimitBurst)
	assert.Empty(t, cfg.TelegramBotToken)
	assert.Empty(t, cfg.TelegramChatID)
}

func TestLoad_ExplicitValues(t *testing.T) {
	env := validEnv()
	env["HTTP_PORT"] = "9090"
	env["LOG_FORMAT"] = "text"
	env["BCRYPT_COST"] = "12"
	env["RATE_LIMIT_RPS"] = "0"
	env["RATE_LIMIT_BURST"] = "1"
	env["TELEGRAM_BOT_TOKEN"] = "123:abc"
	env["TELEGRAM_CHAT_ID"] = "-100200300"

	cfg, err := load(lookupFrom(env))
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.HTTPPort)
	assert.Equal(t, LogFormatText, cfg.LogFormat)
	assert.Equal(t, 12, cfg.BcryptCost)
	assert.Equal(t, 0, cfg.RateLimitRPS, "zero disables the rate limiter")
	assert.Equal(t, 1, cfg.RateLimitBurst)
	assert.Equal(t, "123:abc", cfg.TelegramBotToken)
	assert.Equal(t, "-100200300", cfg.TelegramChatID)
}

func TestLoad_CORSOrigins(t *testing.T) {
	env := validEnv()
	env["CORS_ORIGINS"] = " https://app.example.com , https://admin.example.com ,"

	cfg, err := load(lookupFrom(env))
	require.NoError(t, err)

	assert.Equal(t, []string{"https://app.example.com", "https://admin.example.com"}, cfg.CORSOrigins)
}

func TestLoad_TelegramBothOrNeither(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_BOT_TOKEN"] = "123:abc"

	_, err := load(lookupFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID")
}

func TestLoad_InvalidRateLimits(t *testing.T) {
	env := validEnv()
	env["RATE_LIMIT_RPS"] = "-1"
	env["RATE_LIMIT_BURST"] = "0"

	_, err := load(lookupFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RATE_LIMIT_RPS")
	assert.Contains(t, err.Error(), "RATE_LIMIT_BURST")
}

func TestLoad_ReportsAllProblemsAtOnce(t *testing.T) {
	env := map[string]string{
		"HTTP_PORT":   "not-a-port",
		"LOG_FORMAT":  "xml",
		"BCRYPT_COST": "99",
	}

	_, err := load(lookupFrom(env))
	require.Error(t, err)

	for _, key := range []string{"DATABASE_URL", "JWT_SECRET", "HTTP_PORT", "LOG_FORMAT", "BCRYPT_COST"} {
		assert.Contains(t, err.Error(), key)
	}
}

func TestLoad_ShortJWTSecret(t *testing.T) {
	env := validEnv()
	env["JWT_SECRET"] = "too-short"

	_, err := load(lookupFrom(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SECRET")
	assert.Contains(t, err.Error(), "at least 32 bytes")
}
