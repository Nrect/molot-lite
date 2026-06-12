package bids

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"molotlite/internal/platform/auth"
	"molotlite/internal/platform/errs"
	"molotlite/internal/platform/server"
)

// Handler is the thin HTTP edge of the feature (rule 4).
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterPublic mounts the bid history; r is the /api group from main.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Get("/lots/{lotID}/bids", h.history)
}

// RegisterProtected mounts bid placement behind auth.Middleware.
func (h *Handler) RegisterProtected(r chi.Router) {
	r.Post("/lots/{lotID}/bids", h.place)
}

func (h *Handler) place(w http.ResponseWriter, r *http.Request) {
	lotID, err := lotIDParam(r)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	bidderID, err := auth.UserID(r.Context())
	if err != nil {
		errs.Respond(w, r, err)
		return
	}

	var req struct {
		AmountMinor int64 `json:"amountMinor"`
	}
	if err := server.DecodeJSON(r, &req); err != nil {
		errs.Respond(w, r, err)
		return
	}

	bid, err := h.svc.Place(r.Context(), lotID, bidderID, req.AmountMinor)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusCreated, bid)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	lotID, err := lotIDParam(r)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	page, err := queryInt(r, "page")
	if err != nil {
		errs.Respond(w, r, errs.BadRequest("invalid-page").WithCause(err))
		return
	}
	pageSize, err := queryInt(r, "pageSize")
	if err != nil {
		errs.Respond(w, r, errs.BadRequest("invalid-page-size").WithCause(err))
		return
	}

	res, err := h.svc.History(r.Context(), lotID, page, pageSize)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusOK, res)
}

func lotIDParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "lotID"))
	if err != nil {
		return uuid.Nil, errs.BadRequest("invalid-lot-id").WithCause(err)
	}
	return id, nil
}

// queryInt reads an optional integer query parameter; absent means 0
// (the service applies the default).
func queryInt(r *http.Request, key string) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}
