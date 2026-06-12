// Package notify owns user-facing notifications, delivered through the
// Telegram Bot API. It is a feature folder degenerated to its service:
// no handler (nothing to serve), no storage or migrations (nothing
// persisted) — rule 1 still holds, everything about notifications lives
// here and deleting the folder plus its lines in main removes the
// feature.
//
// Delivery is deliberately best-effort: a missed notification must
// never fail the business operation that caused it, so errors are
// logged and swallowed. When that stops being acceptable (delivery
// guarantees, retries), the maturation path is the outbox from Molot,
// not retry loops here.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Service sends notifications to one Telegram chat — the MVP-typical
// "owner's channel" setup. Empty credentials disable delivery: events
// are logged instead, so local development needs no bot.
type Service struct {
	client *http.Client
	token  string
	chatID string

	// baseURL is the Telegram API host; unexported so only same-package
	// tests can point it at a fake server.
	baseURL string
}

func NewService(token, chatID string) *Service {
	return &Service{
		client:  &http.Client{Timeout: 3 * time.Second},
		token:   token,
		chatID:  chatID,
		baseURL: "https://api.telegram.org",
	}
}

// OutbidEvent describes a bid that displaced the previous top bidder.
type OutbidEvent struct {
	LotID          uuid.UUID
	LotTitle       string
	OutbidUserID   uuid.UUID
	NewAmountMinor int64
}

// Outbid notifies about an outbid user. It never returns an error: the
// bid this event came from has already committed, so a delivery failure
// is the notifier's problem alone and ends up in the log.
func (s *Service) Outbid(ctx context.Context, e OutbidEvent) {
	if s.token == "" || s.chatID == "" {
		slog.InfoContext(ctx, "outbid notification (telegram disabled)",
			slog.String("lot_id", e.LotID.String()),
			slog.String("lot_title", e.LotTitle),
			slog.String("outbid_user_id", e.OutbidUserID.String()),
			slog.Int64("new_amount_minor", e.NewAmountMinor),
		)
		return
	}

	text := fmt.Sprintf("Outbid on %q: user %s lost the top spot, new bid is %d (lot %s)",
		e.LotTitle, e.OutbidUserID, e.NewAmountMinor, e.LotID)
	body, err := json.Marshal(map[string]string{"chat_id": s.chatID, "text": text})
	if err != nil {
		slog.ErrorContext(ctx, "encode telegram message", slog.Any("error", err))
		return
	}

	url := s.baseURL + "/bot" + s.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.ErrorContext(ctx, "build telegram request", slog.Any("error", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "send telegram message", slog.Any("error", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "telegram rejected the message",
			slog.Int("status", resp.StatusCode),
			slog.String("lot_id", e.LotID.String()),
		)
	}
}
