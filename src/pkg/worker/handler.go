package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"vm-controller/src/pkg/models"
)

// Handler sets up the HTTP router for the Worker Agent.
type Handler struct {
	worker *Worker
	mux    *http.ServeMux
}

// NewHandler creates a new HTTP handler for the worker.
func NewHandler(w *Worker) *Handler {
	h := &Handler{
		worker: w,
		mux:    http.NewServeMux(),
	}
	h.registerRoutes()
	return h
}

// ServeHTTP implements http.Handler with Bearer token authentication.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Exclude health endpoint from auth if desired, or require auth for all
	if r.URL.Path == "/health" || r.URL.Path == "/ping" {
		h.mux.ServeHTTP(w, r)
		return
	}

	authHeader := r.Header.Get("Authorization")
	expectedToken := h.worker.cfg.Token
	if expectedToken != "" {
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
	h.mux.HandleFunc("GET /health", h.handleHealth)
	h.mux.HandleFunc("GET /ping", h.handleHealth)
	h.mux.HandleFunc("GET /resources", h.handleResources)
	h.mux.HandleFunc("POST /vms", h.handleCreateVM)
	h.mux.HandleFunc("GET /vms", h.handleListVMs)
	h.mux.HandleFunc("GET /vms/{id}", h.handleGetVM)
	h.mux.HandleFunc("POST /vms/{id}/start", h.handleStartVM)
	h.mux.HandleFunc("POST /vms/{id}/stop", h.handleStopVM)
	h.mux.HandleFunc("DELETE /vms/{id}", h.handleDeleteVM)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"worker_id": h.worker.cfg.ID,
	})
}

func (h *Handler) handleResources(w http.ResponseWriter, r *http.Request) {
	res := h.worker.GetResources()
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req models.CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Error:   "invalid json payload: " + err.Error(),
		})
		return
	}

	vm, err := h.worker.CreateVM(r.Context(), req)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "validation") {
			status = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "insufficient memory") {
			status = http.StatusInsufficientStorage
		}
		writeJSON(w, status, models.APIResponse{
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

func (h *Handler) handleListVMs(w http.ResponseWriter, r *http.Request) {
	vms := h.worker.ListVMs()
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    vms,
	})
}

func (h *Handler) handleGetVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vm, err := h.worker.GetVM(id)
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
	err := h.worker.BootVM(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, models.ErrInvalidTransition) || strings.Contains(err.Error(), "already running") {
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	vm, _ := h.worker.GetVM(id)
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "vm started successfully",
		Data:    vm,
	})
}

func (h *Handler) handleStopVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.worker.StopVM(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, models.ErrInvalidTransition) {
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	vm, _ := h.worker.GetVM(id)
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "vm stopped successfully",
		Data:    vm,
	})
}

func (h *Handler) handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.worker.DeleteVM(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "vm deleted successfully",
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
