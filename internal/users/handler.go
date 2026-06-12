package users

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"molotlite/internal/platform/auth"
	"molotlite/internal/platform/errs"
	"molotlite/internal/platform/server"
)

// Handler is the thin HTTP edge of the feature (rule 4): decode, call
// the service, map the error, encode.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterPublic mounts the routes that need no token; r is the /api
// group built in main.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Post("/users", h.register)
	r.Post("/users/login", h.login)
}

// RegisterProtected mounts the routes behind auth.Middleware.
func (h *Handler) RegisterProtected(r chi.Router) {
	r.Get("/users/me", h.me)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if err := server.DecodeJSON(r, &req); err != nil {
		errs.Respond(w, r, err)
		return
	}

	id, err := h.svc.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusCreated, map[string]string{"id": id.String()})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := server.DecodeJSON(r, &req); err != nil {
		errs.Respond(w, r, err)
		return
	}

	token, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.UserID(r.Context())
	if err != nil {
		errs.Respond(w, r, err)
		return
	}

	profile, err := h.svc.Profile(r.Context(), userID)
	if err != nil {
		errs.Respond(w, r, err)
		return
	}
	server.RespondJSON(w, http.StatusOK, profile)
}
