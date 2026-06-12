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
}

func TestLoad_ExplicitValues(t *testing.T) {
	env := validEnv()
	env["HTTP_PORT"] = "9090"
	env["LOG_FORMAT"] = "text"
	env["BCRYPT_COST"] = "12"

	cfg, err := load(lookupFrom(env))
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.HTTPPort)
	assert.Equal(t, LogFormatText, cfg.LogFormat)
	assert.Equal(t, 12, cfg.BcryptCost)
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
