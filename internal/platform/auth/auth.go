// Package auth provides JWT issuing and the bearer-token middleware of
// the HTTP stack. The platform only mints and validates tokens;
// password hashing and login live in the feature that owns users.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"molotlite/internal/platform/errs"
)

type userIDKey struct{}

// ContextWithUserID returns ctx carrying the authenticated user id.
// The middleware calls it on every authenticated request; tests use it
// to fabricate authenticated contexts without HTTP.
func ContextWithUserID(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserID returns the authenticated user id placed in ctx by the
// middleware. A missing id means the route was wired outside the auth
// middleware — a programming error, surfaced as KindInternal (500).
func UserID(ctx context.Context) (uuid.UUID, error) {
	id, ok := ctx.Value(userIDKey{}).(uuid.UUID)
	if !ok {
		return uuid.Nil, errs.Internal("no-user-in-context")
	}
	return id, nil
}

// GenerateToken issues an HS256 token for userID, valid for ttl. It is
// the login/test counterpart of the middleware and shares its claims
// layout, so a generated token always round-trips through validation.
// A negative ttl produces an already-expired token (for tests).
func GenerateToken(secret string, userID uuid.UUID, ttl time.Duration) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	})

	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Middleware validates the Bearer JWT (HS256, sub = user uuid, exp
// required) and puts the user id into the request context. Requests
// that fail validation receive 401 {"slug":"unauthorized"} through the
// shared errs mapper.
func Middleware(secret string) func(http.Handler) http.Handler {
	secretBytes := []byte(secret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, err := userIDFromRequest(r, secretBytes)
			if err != nil {
				errs.Respond(w, r, errs.Unauthorized("unauthorized").WithCause(err))
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithUserID(r.Context(), userID)))
		})
	}
}

func userIDFromRequest(r *http.Request, secret []byte) (uuid.UUID, error) {
	tokenString, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tokenString == "" {
		return uuid.Nil, errors.New("missing bearer token")
	}

	var c jwt.RegisteredClaims
	token, err := jwt.ParseWithClaims(tokenString, &c,
		func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return uuid.Nil, errors.New("invalid token")
	}

	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("sub claim is not a uuid: %w", err)
	}
	return id, nil
}
