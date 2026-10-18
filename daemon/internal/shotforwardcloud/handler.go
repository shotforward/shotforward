package shotforwardcloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const cloudReadTimeout = 30 * time.Second

type Handler struct {
	manager *Manager
	logger  *slog.Logger
}

func NewHandler(manager *Manager, logger *slog.Logger) (*Handler, error) {
	if manager == nil || logger == nil {
		return nil, errors.New("ShotForward cloud manager and logger are required")
	}
	manager.mu.Lock()
	manager.proxyLogger = logger
	manager.mu.Unlock()
	return &Handler{manager: manager, logger: logger}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v1/cloud/openai"):
		h.manager.ProxyOpenAI(w, r, strings.TrimPrefix(r.URL.Path, "/api/v1/cloud/openai"))
	case r.URL.Path == "/api/v1/cloud/account" && r.Method == http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), cloudReadTimeout)
		defer cancel()
		account, err := h.manager.Account(ctx)
		if err != nil {
			h.logFailure(r, "load_account", err)
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, account)
	case r.URL.Path == "/api/v1/cloud/models" && r.Method == http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), cloudReadTimeout)
		defer cancel()
		models, err := h.manager.Models(ctx)
		if err != nil {
			h.logFailure(r, "list_models", err)
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	case r.URL.Path == "/api/v1/cloud/auth/exchanges" && r.Method == http.MethodPost:
		var request struct {
			Code string `json:"code"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(request.Code) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_argument", "message": "login code is required"}})
			return
		}
		if err := h.manager.ExchangeDesktopCode(r.Context(), request.Code); err != nil {
			h.logFailure(r, "exchange_desktop_code", err)
			writeError(w, err)
			return
		}
		account, err := h.manager.Account(r.Context())
		if err != nil {
			h.logFailure(r, "load_account_after_exchange", err)
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, account)
	case r.URL.Path == "/api/v1/cloud/auth/logout" && r.Method == http.MethodPost:
		if err := h.manager.Logout(r.Context()); err != nil {
			h.logFailure(r, "clear_local_session", err)
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/v1/cloud/auth/reset" && r.Method == http.MethodPost:
		if err := h.manager.ForgetSession(r.Context()); err != nil {
			h.logFailure(r, "reset_local_session", err)
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *Handler) logFailure(r *http.Request, operation string, err error) {
	h.logger.WarnContext(r.Context(), "ShotForward cloud request failed",
		"operation", operation,
		"request_id", r.Header.Get("X-Request-ID"),
		"error", err,
	)
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "cloud_unavailable"
	if errors.Is(err, ErrInvalidLoginCode) {
		status, code = http.StatusUnauthorized, "invalid_desktop_code"
	} else if errors.Is(err, ErrLoginRequired) {
		status, code = http.StatusUnauthorized, "cloud_login_required"
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
