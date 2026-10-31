package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"vm-controller/src/pkg/models"
)

// Handler sets up HTTP routing and Bearer token auth for the Controller.
type Handler struct {
	controller *Controller
	mux        *http.ServeMux
}

// NewHandler creates a new HTTP handler for the Controller.
func NewHandler(c *Controller) *Handler {
	h := &Handler{
		controller: c,
		mux:        http.NewServeMux(),
	}
	h.registerRoutes()
	return h
}

// ServeHTTP applies Bearer token authentication and dispatches requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/ui" || r.URL.Path == "/health" {
		h.mux.ServeHTTP(w, r)
		return
	}

	expectedToken := h.controller.cfg.Token
	if expectedToken != "" {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") || strings.TrimPrefix(authHeader, "Bearer ") != expectedToken {
			writeJSON(w, http.StatusUnauthorized, models.APIResponse{
				Success: false,
				Error:   "unauthorized: valid Bearer token required",
			})
			return
		}
	}

	h.mux.ServeHTTP(w, r)
}

func (h *Handler) registerRoutes() {
	h.mux.HandleFunc("GET /{$}", h.handleUI)
	h.mux.HandleFunc("GET /ui", h.handleUI)
	h.mux.HandleFunc("GET /health", h.handleHealth)
	h.mux.HandleFunc("GET /workers", h.handleGetWorkers)
	h.mux.HandleFunc("GET /hosts", h.handleGetWorkers)
	h.mux.HandleFunc("POST /vms", h.handleCreateVM)
	h.mux.HandleFunc("GET /vms", h.handleListVMs)
	h.mux.HandleFunc("GET /vms/{id}", h.handleGetVM)
	h.mux.HandleFunc("POST /vms/{id}/start", h.handleStartVM)
	h.mux.HandleFunc("POST /vms/{id}/stop", h.handleStopVM)
	h.mux.HandleFunc("DELETE /vms/{id}", h.handleDeleteVM)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"role":   "controller",
	})
}

func (h *Handler) handleGetWorkers(w http.ResponseWriter, r *http.Request) {
	workers := h.controller.GetWorkerInfos(r.Context())
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    workers,
	})
}

func (h *Handler) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req models.CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Error:   "invalid JSON payload: " + err.Error(),
		})
		return
	}

	vm, err := h.controller.CreateVM(r.Context(), req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrInsufficientMemory) || strings.Contains(err.Error(), "insufficient memory") {
			status = http.StatusInsufficientStorage
		} else if strings.Contains(err.Error(), "validation") || strings.Contains(err.Error(), "unhealthy") || strings.Contains(err.Error(), "not found") {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "VM created successfully",
		Data:    vm,
	})
}

func (h *Handler) handleListVMs(w http.ResponseWriter, r *http.Request) {
	vms := h.controller.ListVMs()
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    vms,
	})
}

func (h *Handler) handleGetVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vm, err := h.controller.GetVM(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    vm,
	})
}

func (h *Handler) handleStartVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vm, err := h.controller.StartVM(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, models.ErrInvalidTransition) || errors.Is(err, ErrAlreadyRunning) {
			status = http.StatusConflict
		} else if errors.Is(err, ErrVMNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "VM started successfully",
		Data:    vm,
	})
}

func (h *Handler) handleStopVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vm, err := h.controller.StopVM(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, models.ErrInvalidTransition) {
			status = http.StatusConflict
		} else if errors.Is(err, ErrVMNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "VM stopped successfully",
		Data:    vm,
	})
}

func (h *Handler) handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.controller.DeleteVM(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, models.ErrInvalidTransition) {
			status = http.StatusConflict
		} else if errors.Is(err, ErrVMNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "VM deleted successfully",
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
