package logs

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextHandler_AddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(&buf, nil)))

	ctx := ContextWithRequestID(context.Background(), "req-42")
	logger.InfoContext(ctx, "hello")

	assert.Contains(t, buf.String(), `"request_id":"req-42"`)
}

func TestContextHandler_NoRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(&buf, nil)))

	logger.InfoContext(context.Background(), "hello")

	assert.NotContains(t, buf.String(), "request_id")
}

func TestRequestIDFromContext(t *testing.T) {
	_, ok := RequestIDFromContext(context.Background())
	require.False(t, ok)

	id, ok := RequestIDFromContext(ContextWithRequestID(context.Background(), "req-1"))
	require.True(t, ok)
	assert.Equal(t, "req-1", id)
}
