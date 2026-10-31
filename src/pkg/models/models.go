package models

import (
	"errors"
	"fmt"
	"time"
)

// VMState represents deterministic states of a VM in the lifecycle state machine.
type VMState string

const (
	StateCreating VMState = "CREATING"
	StateCreated  VMState = "CREATED"
	StateRunning  VMState = "RUNNING"
	StateStopped  VMState = "STOPPED"
	StateDeleted  VMState = "DELETED"
	StateError    VMState = "ERROR"
)

// ErrInvalidTransition is returned when an illegal state transition is attempted.
var ErrInvalidTransition = errors.New("invalid state transition")

// IsValidTransition checks whether moving from currentState to targetState is permitted.
func IsValidTransition(from, to VMState) bool {
	switch from {
	case StateCreating:
		return to == StateCreated || to == StateError
	case StateCreated:
		return to == StateRunning || to == StateDeleted || to == StateError
	case StateRunning:
		return to == StateStopped || to == StateError
	case StateStopped:
		return to == StateRunning || to == StateDeleted || to == StateError
	case StateError:
		return to == StateDeleted || to == StateStopped
	default:
		return false
	}
}

// VMResourceUsage captures live CPU and memory metrics for an individual VM.
type VMResourceUsage struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMB  int     `json:"memory_used_mb"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
	MemoryPercent float64 `json:"memory_percent"`
}

// VM holds the metadata, configuration, and current state of a virtual machine.
type VM struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	VCPUs         int              `json:"vcpus"`
	MemoryMB      int              `json:"memory_mb"`
	OS            string           `json:"os"`
	IPAddress     string           `json:"ip_address,omitempty"`
	SSHPort       int              `json:"ssh_port,omitempty"`
	SerialSocket  string           `json:"serial_socket,omitempty"`
	State         VMState          `json:"state"`
	HostID        string           `json:"host_id,omitempty"`
	ErrorMessage  string           `json:"error_message,omitempty"`
	ResourceUsage *VMResourceUsage `json:"resource_usage,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// CreateVMRequest is the request payload to instantiate a new VM.
type CreateVMRequest struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb"`
	OS       string `json:"os,omitempty"`
	WorkerID string `json:"worker_id,omitempty"`
	AutoBoot bool   `json:"auto_boot,omitempty"`
}

// Validate checks resource limits defined in the specification:
// vCPU: 1-4, Memory: 128-2048 MB.
func (r *CreateVMRequest) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.VCPUs < 1 || r.VCPUs > 4 {
		return fmt.Errorf("vcpus must be between 1 and 4, got %d", r.VCPUs)
	}
	if r.MemoryMB < 128 || r.MemoryMB > 2048 {
		return fmt.Errorf("memory_mb must be between 128 and 2048 MB, got %d", r.MemoryMB)
	}
	return nil
}

// HostResources represents the health and resource capacity of a worker host.
type HostResources struct {
	WorkerID          string             `json:"worker_id"`
	Healthy           bool               `json:"healthy"`
	TotalMemoryMB     int                `json:"total_memory_mb"`
	AvailableMemoryMB int                `json:"available_memory_mb"`
	FreeMemoryMB      int                `json:"free_memory_mb"`
	ReservedMemoryMB  int                `json:"reserved_memory_mb"`
	RunningVMs        int                `json:"running_vms"`
	TotalVMs          int                `json:"total_vms"`
	VMs               map[string]VMState `json:"vms,omitempty"`
}

// WorkerRegistration contains config info for a worker known to the controller.
type WorkerInfo struct {
	ID               string        `json:"id"`
	URL              string        `json:"url"`
	Healthy          bool          `json:"healthy"`
	LastHeartbeat    time.Time     `json:"last_heartbeat"`
	ConsecutiveFails int           `json:"consecutive_fails"`
	Resources        HostResources `json:"resources"`
}

// APIResponse is standard JSON envelope for responses.
type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}
