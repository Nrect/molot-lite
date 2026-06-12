// Same-package tests: they reach the unexported baseURL to point the
// service at a fake Telegram, keeping the API key requirement out of
// the suite.
package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func event() OutbidEvent {
	return OutbidEvent{
		LotID:          uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		LotTitle:       "Antique hammer",
		OutbidUserID:   uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		NewAmountMinor: 15_000,
	}
}

func TestOutbid_SendsTelegramMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	svc := NewService("123:abc", "-100200300")
	svc.baseURL = fake.URL

	svc.Outbid(context.Background(), event())

	assert.Equal(t, "/bot123:abc/sendMessage", gotPath)
	assert.Equal(t, "-100200300", gotBody["chat_id"])
	assert.Contains(t, gotBody["text"], "Antique hammer")
	assert.Contains(t, gotBody["text"], "15000")
	assert.Contains(t, gotBody["text"], "22222222-2222-2222-2222-222222222222")
}

func TestOutbid_ServerErrorDoesNotPanic(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fake.Close()

	svc := NewService("123:abc", "-100200300")
	svc.baseURL = fake.URL

	// The contract is "errors stay inside": nothing to assert beyond
	// the call completing without a panic or a returned error.
	svc.Outbid(context.Background(), event())
}

func TestOutbid_DisabledMakesNoRequests(t *testing.T) {
	calls := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	svc := NewService("", "")
	svc.baseURL = fake.URL

	svc.Outbid(context.Background(), event())

	assert.Zero(t, calls, "empty credentials must keep the notifier offline")
}
