package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"molotlite/internal/platform/errs"
)

// maxBodyBytes caps request bodies read by DecodeJSON; 1 MiB is plenty
// for any JSON API payload and shields the decoder from junk uploads.
const maxBodyBytes = 1 << 20

// DecodeJSON decodes the request body into dst. Malformed JSON and
// trailing garbage surface as errs.BadRequest("invalid-json"), a body
// over 1 MiB as errs.BadRequest("body-too-large") — handlers pass the
// error straight to errs.Respond.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return errs.BadRequest("body-too-large").WithCause(err)
		}
		return errs.BadRequest("invalid-json").WithCause(err)
	}
	if dec.More() {
		return errs.BadRequest("invalid-json").WithCause(errors.New("trailing data after JSON value"))
	}
	return nil
}

// RespondJSON writes v as the JSON response body with the given status
// code. A nil v writes the status line only (e.g. 204).
func RespondJSON(w http.ResponseWriter, code int, v any) {
	if v == nil {
		w.WriteHeader(code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
