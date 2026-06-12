package errs

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRespond(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"bad request", BadRequest("invalid-json"), http.StatusBadRequest, `{"slug":"invalid-json"}`},
		{"unauthorized", Unauthorized("unauthorized"), http.StatusUnauthorized, `{"slug":"unauthorized"}`},
		{"forbidden", Forbidden("not-owner"), http.StatusForbidden, `{"slug":"not-owner"}`},
		{"not found", NotFound("lot-not-found"), http.StatusNotFound, `{"slug":"lot-not-found"}`},
		{"conflict", Conflict("already-paid"), http.StatusConflict, `{"slug":"already-paid"}`},
		{"internal", Internal("storage-broken"), http.StatusInternalServerError, `{"slug":"storage-broken"}`},
		{"plain error", errors.New("boom"), http.StatusInternalServerError, `{"slug":"internal-server-error"}`},
		{"wrapped slug error", fmt.Errorf("get lot: %w", NotFound("lot-not-found")), http.StatusNotFound, `{"slug":"lot-not-found"}`},
		{"slug error with cause", NotFound("lot-not-found").WithCause(errors.New("no rows")), http.StatusNotFound, `{"slug":"lot-not-found"}`},
		{"nil error", nil, http.StatusInternalServerError, `{"slug":"internal-server-error"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			Respond(w, r, tt.err)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.JSONEq(t, tt.wantBody, w.Body.String())
			assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
		})
	}
}

func TestSlugError_Is(t *testing.T) {
	err := fmt.Errorf("layer: %w", Conflict("already-paid").WithCause(errors.New("db detail")))

	assert.ErrorIs(t, err, Conflict("already-paid"))
	assert.NotErrorIs(t, err, Conflict("other-slug"))
	assert.NotErrorIs(t, err, NotFound("already-paid"))
}

func TestSlugError_UnwrapsCause(t *testing.T) {
	cause := errors.New("no rows")
	err := NotFound("lot-not-found").WithCause(cause)

	require.ErrorIs(t, err, cause)
	assert.Equal(t, "lot-not-found: no rows", err.Error())
}

func TestKindFromError(t *testing.T) {
	assert.Equal(t, KindNotFound, KindFromError(NotFound("x")))
	assert.Equal(t, KindInternal, KindFromError(errors.New("plain")))
	assert.Equal(t, KindInternal, KindFromError(SlugError{Slug: "no-kind"}))
}
