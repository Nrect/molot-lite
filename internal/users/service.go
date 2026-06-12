// Package users owns registration, login and profiles: the users table,
// password hashing and issuing of session JWTs. Other features that
// need user data call its service functions (rule 2) instead of
// querying the users table.
package users

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"molotlite/internal/platform/auth"
	"molotlite/internal/platform/errs"
)

// tokenTTL is how long a login session lasts.
const tokenTTL = 24 * time.Hour

const (
	minPasswordBytes = 8
	// bcrypt hashes at most 72 bytes of input; longer passwords are
	// rejected instead of being silently truncated.
	maxPasswordBytes = 72
)

// Service implements the users feature as plain transaction scripts
// (rule 3): validate, hash, store, return.
type Service struct {
	pool       *pgxpool.Pool
	jwtSecret  string
	bcryptCost int

	// dummyHash keeps the unknown-email branch of Login doing the same
	// bcrypt work as the known-email branch, so response timing does
	// not reveal whether an address is registered.
	dummyHash []byte
}

func NewService(pool *pgxpool.Pool, jwtSecret string, bcryptCost int) *Service {
	dummyHash, err := bcrypt.GenerateFromPassword([]byte(uuid.NewString()), bcryptCost)
	if err != nil {
		// GenerateFromPassword fails only on a cost outside the bcrypt
		// range, which config.Load already validates: programming error.
		panic(fmt.Sprintf("users: dummy hash: %v", err))
	}
	return &Service{pool: pool, jwtSecret: jwtSecret, bcryptCost: bcryptCost, dummyHash: dummyHash}
}

// User is the public profile. There is deliberately no field for the
// password hash.
type User struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Register validates the input, hashes the password and creates the
// user. A taken email surfaces as errs.Conflict("email-taken").
func (s *Service) Register(ctx context.Context, email, password, displayName string) (uuid.UUID, error) {
	email, err := validEmail(email)
	if err != nil {
		return uuid.Nil, err
	}
	if len(password) < minPasswordBytes {
		return uuid.Nil, errs.BadRequest("password-too-short")
	}
	if len(password) > maxPasswordBytes {
		return uuid.Nil, errs.BadRequest("password-too-long")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return uuid.Nil, errs.BadRequest("display-name-required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return uuid.Nil, fmt.Errorf("hash password: %w", err)
	}

	row := userRow{id: uuid.New(), email: email, passwordHash: string(hash), displayName: displayName}
	if err := insertUser(ctx, s.pool, row); err != nil {
		return uuid.Nil, err
	}
	return row.id, nil
}

// Login verifies the credentials and returns a session JWT. An unknown
// email and a wrong password are indistinguishable to the caller: same
// slug, same status, comparable timing (see dummyHash).
func (s *Service) Login(ctx context.Context, email, password string) (string, error) {
	row, err := getUserByEmail(ctx, s.pool, normalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return "", errs.Unauthorized("invalid-credentials").WithCause(err)
	}
	if err != nil {
		return "", err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(row.passwordHash), []byte(password)); err != nil {
		return "", errs.Unauthorized("invalid-credentials").WithCause(err)
	}

	token, err := auth.GenerateToken(s.jwtSecret, row.id, tokenTTL)
	if err != nil {
		return "", fmt.Errorf("issue token: %w", err)
	}
	return token, nil
}

// Profile returns the public profile of the given user.
func (s *Service) Profile(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := getUserByID(ctx, s.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, errs.NotFound("user-not-found").WithCause(err)
	}
	if err != nil {
		return User{}, err
	}
	return User{ID: row.id, Email: row.email, DisplayName: row.displayName, CreatedAt: row.createdAt}, nil
}

// UserExists reports whether a user with the given id exists. This is
// the service function other features call instead of touching the
// users table (rule 2: a plain call, no interfaces, no ceremony).
func (s *Service) UserExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return userExists(ctx, s.pool, id)
}

func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// validEmail normalizes and validates the address. The parsed address
// must round-trip exactly, which rejects forms like "Bob <bob@host>".
func validEmail(raw string) (string, error) {
	email := normalizeEmail(raw)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", errs.BadRequest("invalid-email")
	}
	return email, nil
}
