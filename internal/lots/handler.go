package lots

import (
	"net/http"
	"strconv"
	"time"

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

// RegisterPublic mounts browsing routes; r is the /api group from main.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Get("/lots", h.list)
	r.Get("/lots/{lotID}", h.get)
}

// RegisterProtected mounts the owner actions behind auth.Middleware.
func (h *Handler) RegisterProtected(r chi.Router) {
	r.Post("/lots", h.create)
	r.Patch("/lots/{lotID}", h.update)
	r.Delete("/lots/{lotID}", h.delete)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ownerID, err := auth.UserID(r.Context())
	if err != nil {
		errs.Respond(w, r, err)
		return
	}

	var req struct {
		Title           string    `json:"title"`
		Description     string    `json:"description"`
		StartPriceMinor int64     `json:"startPriceMinor"`
		Currency        string    `json:"currency"`
		EndsAt          time.Time `json:"endsAt"`
	}
	if err := server.DecodeJSON(r, &req); err != nil {
		errs.Respond(w, r, err)
		return
	}

	id, err := h.svc.Create(r.Context(), ownerID, CreateInput{
		Title:           req.Title,
		Description:     req.Description,
		StartPriceMinor: req.StartPriceMinor,
		Currency:        req.Currency,
		EndsAt:          req.EndsAt,
	})
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusCreated, map[string]string{"id": id.String()})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
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

	res, err := h.svc.List(r.Context(), ListInput{
		Page:     page,
		PageSize: pageSize,
		Query:    r.URL.Query().Get("q"),
	})
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusOK, res)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	lotID, err := lotIDParam(r)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}

	lot, err := h.svc.Get(r.Context(), lotID)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusOK, lot)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	lotID, err := lotIDParam(r)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	callerID, err := auth.UserID(r.Context())
	if err != nil {
		errs.Respond(w, r, err)
		return
	}

	var req struct {
		Title           *string    `json:"title"`
		Description     *string    `json:"description"`
		StartPriceMinor *int64     `json:"startPriceMinor"`
		EndsAt          *time.Time `json:"endsAt"`
	}
	if err := server.DecodeJSON(r, &req); err != nil {
		errs.Respond(w, r, err)
		return
	}

	lot, err := h.svc.Update(r.Context(), lotID, callerID, UpdateInput{
		Title:           req.Title,
		Description:     req.Description,
		StartPriceMinor: req.StartPriceMinor,
		EndsAt:          req.EndsAt,
	})
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusOK, lot)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	lotID, err := lotIDParam(r)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	callerID, err := auth.UserID(r.Context())
	if err != nil {
		errs.Respond(w, r, err)
		return
	}

	if err := h.svc.Delete(r.Context(), lotID, callerID); err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusNoContent, nil)
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
