package worker

import (
	"context"
	"vm-controller/src/pkg/models"
)

// HypervisorDriver defines the interface for interacting with the VM hypervisor.
type HypervisorDriver interface {
	// CreateVM prepares resources (disks, sockets) and configures the VM.
	CreateVM(ctx context.Context, vm *models.VM) error

	// BootVM starts execution of the virtual machine.
	BootVM(ctx context.Context, vmID string) error

	// StopVM stops/shuts down a running virtual machine.
	StopVM(ctx context.Context, vmID string) error

	// DeleteVM completely cleans up the virtual machine, stopping processes and deleting files.
	DeleteVM(ctx context.Context, vmID string) error

	// GetVMStatus inspects the current hypervisor status for a VM.
	GetVMStatus(ctx context.Context, vmID string) (models.VMState, error)

	// CleanupOrphans finds and cleans up any leaked sockets, files, or orphaned processes.
	CleanupOrphans(ctx context.Context, activeVMIDs []string) error

	// RecoverVM re-attaches a previously configured VM instance on worker startup.
	RecoverVM(ctx context.Context, vm *models.VM) error

	// GetVMResourceUsage returns real-time CPU and memory metrics for a VM.
	GetVMResourceUsage(ctx context.Context, vmID string) (*models.VMResourceUsage, error)
}
