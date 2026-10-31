package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	"vm-controller/src/pkg/models"
)

// Reconcile triggers a single reconciliation pass immediately.
func (c *Controller) Reconcile() {
	c.reconcile()
}

// reconcilerLoop periodically checks worker health and reconciles VM states.
func (c *Controller) reconcilerLoop() {
	// Perform immediate sync on startup
	c.reconcile()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			c.reconcile()
		}
	}
}

// syncWorkerVMs queries GET /vms from a worker to retrieve its full active VM list.
func (c *Controller) syncWorkerVMs(ctx context.Context, t WorkerTarget) ([]*models.VM, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL+"/vms", nil)
	if err != nil {
		return nil, err
	}
	if t.Token != "" {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("worker %s returned HTTP %d", t.ID, resp.StatusCode)
	}

	var apiResp struct {
		Success bool         `json:"success"`
		Data    []*models.VM `json:"data"`
		Error   string       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}
	if !apiResp.Success {
		return nil, fmt.Errorf("worker %s error: %s", t.ID, apiResp.Error)
	}

	return apiResp.Data, nil
}

// reconcile polls all workers and reconciles VM records.
func (c *Controller) reconcile() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	// Update worker capacities and health
	workerInfos := c.GetWorkerInfos(ctx)
	workerHealthMap := make(map[string]bool)

	for _, w := range workerInfos {
		workerHealthMap[w.ID] = w.Healthy
	}

	c.mu.RLock()
	targets := make([]WorkerTarget, 0, len(c.workerTargets))
	for _, t := range c.workerTargets {
		targets = append(targets, t)
	}
	c.mu.RUnlock()

	// For each healthy worker, query active VMs
	discoveredWorkerVMs := make(map[string]map[string]*models.VM)
	for _, target := range targets {
		if !workerHealthMap[target.ID] {
			continue
		}
		vms, err := c.syncWorkerVMs(ctx, target)
		if err != nil {
			log.Printf("[Controller Reconciler] Failed to fetch VMs from worker %s: %v", target.ID, err)
			continue
		}
		discoveredWorkerVMs[target.ID] = make(map[string]*models.VM)
		for _, v := range vms {
			discoveredWorkerVMs[target.ID][v.ID] = v
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 1. Adopt or update VMs discovered from healthy workers
	for workerID, vmsOnWorker := range discoveredWorkerVMs {
		for id, workerVM := range vmsOnWorker {
			existingVM, found := c.vms[id]
			if !found {
				// VM exists on worker but not in controller -> Adopt it!
				log.Printf("[Controller Reconciler] Adopted VM %s (state: %s) from worker %s", id, workerVM.State, workerID)
				c.vms[id] = workerVM
				c.saveVMLocked(workerVM)
			} else {
				// Existing VM: sync live attributes from worker
				existingVM.State = workerVM.State
				existingVM.IPAddress = workerVM.IPAddress
				existingVM.SSHPort = workerVM.SSHPort
				existingVM.SerialSocket = workerVM.SerialSocket
				existingVM.ErrorMessage = workerVM.ErrorMessage
				existingVM.ResourceUsage = workerVM.ResourceUsage
				existingVM.UpdatedAt = workerVM.UpdatedAt
				existingVM.HostID = workerID
				c.saveVMLocked(existingVM)
			}
		}
	}

	// 2. Check controller VMs whose host is down or whose host no longer lists the VM
	for id, vm := range c.vms {
		// Only reconcile active non-transient states
		if vm.State != models.StateRunning && vm.State != models.StateCreated {
			continue
		}

		isHostHealthy := workerHealthMap[vm.HostID]
		if !isHostHealthy {
			log.Printf("[Controller Reconciler] Host %s for VM %s is UNHEALTHY/DOWN. Updating VM to ERROR.", vm.HostID, id)
			vm.State = models.StateError
			vm.ErrorMessage = "host " + vm.HostID + " is down or unreachable"
			vm.UpdatedAt = time.Now()
			c.saveVMLocked(vm)
			continue
		}

		vmsOnHost, queried := discoveredWorkerVMs[vm.HostID]
		if queried {
			if _, found := vmsOnHost[id]; !found {
				// VM disappeared from host
				log.Printf("[Controller Reconciler] VM %s missing from host %s. Marking ERROR.", id, vm.HostID)
				vm.State = models.StateError
				vm.ErrorMessage = "vm missing from host hypervisor"
				vm.UpdatedAt = time.Now()
				c.saveVMLocked(vm)
			}
		}
	}
}
