package controller

import (
	"context"
	"fmt"
	"vm-controller/src/pkg/models"
)

// ErrInsufficientMemory is returned when neither worker has enough capacity.
var ErrInsufficientMemory = fmt.Errorf("insufficient memory across all healthy hosts (512MB reserve required)")

// PlacementEngine decides which worker host should run a VM.
type PlacementEngine struct {
	controller *Controller
}

// NewPlacementEngine initializes the placement engine.
func NewPlacementEngine(c *Controller) *PlacementEngine {
	return &PlacementEngine{controller: c}
}

// SelectHost queries both workers, filters by health and memory, and selects
// the host with the most free memory (tie-breaking by fewest running VMs),
// or assigns to the explicitly requested worker if specified.
func (p *PlacementEngine) SelectHost(ctx context.Context, req models.CreateVMRequest) (*models.WorkerInfo, error) {
	// Query current resources from all registered workers
	workerInfos := p.controller.GetWorkerInfos(ctx)
	if len(workerInfos) == 0 {
		return nil, fmt.Errorf("no workers registered in controller")
	}

	// 1. If user explicitly selected a target worker:
	if req.WorkerID != "" && req.WorkerID != "auto" {
		for _, w := range workerInfos {
			if w.ID == req.WorkerID {
				if !w.Healthy {
					return nil, fmt.Errorf("selected worker %s is unhealthy or offline", req.WorkerID)
				}
				pendingReserve := p.controller.getPendingReservation(w.ID)
				effectiveFreeMem := w.Resources.FreeMemoryMB - pendingReserve
				if effectiveFreeMem < req.MemoryMB {
					return nil, fmt.Errorf("selected worker %s has insufficient memory: requires %d MB, has %d MB available (512MB reserve required)",
						req.WorkerID, req.MemoryMB, effectiveFreeMem)
				}
				return w, nil
			}
		}
		return nil, fmt.Errorf("selected worker %s not found", req.WorkerID)
	}

	// 2. Default: automatic memory-aware placement
	var bestWorker *models.WorkerInfo
	bestFreeMem := -1
	bestRunningVMs := 999999

	for _, w := range workerInfos {
		if !w.Healthy {
			continue
		}

		// Calculate available memory taking into account pending in-flight reservations
		pendingReserve := p.controller.getPendingReservation(w.ID)
		effectiveFreeMem := w.Resources.FreeMemoryMB - pendingReserve

		// Placement condition: host must have enough free memory for the VM
		if effectiveFreeMem < req.MemoryMB {
			continue
		}

		// Selection rule:
		// 1. Choose host with the most free memory
		// 2. Tie-break: choose host running fewer VMs
		isBetter := false
		if effectiveFreeMem > bestFreeMem {
			isBetter = true
		} else if effectiveFreeMem == bestFreeMem {
			if w.Resources.RunningVMs < bestRunningVMs {
				isBetter = true
			}
		}

		if isBetter {
			bestWorker = w
			bestFreeMem = effectiveFreeMem
			bestRunningVMs = w.Resources.RunningVMs
		}
	}

	if bestWorker == nil {
		return nil, ErrInsufficientMemory
	}

	return bestWorker, nil
}
