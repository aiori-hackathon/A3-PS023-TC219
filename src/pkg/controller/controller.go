package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"vm-controller/src/pkg/models"
	"vm-controller/src/pkg/store"
)

// ErrAlreadyRunning is returned when attempting to start a running VM.
var ErrAlreadyRunning = errors.New("vm is already running")

// ErrVMNotFound is returned when VM ID does not exist in registry.
var ErrVMNotFound = errors.New("vm not found")

// WorkerTarget contains URL and authorization token for a managed worker.
type WorkerTarget struct {
	ID    string
	URL   string
	Token string
}

// Config configures the central controller.
type Config struct {
	Addr    string
	Token   string
	DataDir string
	Store   store.Store
	Workers []WorkerTarget
}

// Controller coordinates VM lifecycles across worker agents.
type Controller struct {
	cfg                 Config
	store               store.Store
	mu                  sync.RWMutex
	vms                 map[string]*models.VM
	workers             map[string]*models.WorkerInfo
	workerTargets       map[string]WorkerTarget
	pendingReservations map[string]int
	placement           *PlacementEngine
	httpClient          *http.Client
	closeCh             chan struct{}
	closeOnce           sync.Once
	wg                  sync.WaitGroup
}

// NewController initializes a new Central VM Controller.
func NewController(cfg Config) *Controller {
	if cfg.DataDir == "" && cfg.Store == nil {
		cfg.DataDir = filepath.Join(os.TempDir(), "vm-controller")
	}
	if cfg.DataDir != "" {
		_ = os.MkdirAll(cfg.DataDir, 0755)
	}

	var s store.Store = cfg.Store
	if s == nil && cfg.DataDir != "" {
		dbPath := filepath.Join(cfg.DataDir, "controller.db")
		sqliteStore, err := store.NewSQLiteStore(dbPath)
		if err != nil {
			log.Printf("[Controller] Warning: failed to initialize SQLite database at %s: %v", dbPath, err)
		} else {
			s = sqliteStore
		}
	}

	c := &Controller{
		cfg:                 cfg,
		store:               s,
		vms:                 make(map[string]*models.VM),
		workers:             make(map[string]*models.WorkerInfo),
		workerTargets:       make(map[string]WorkerTarget),
		pendingReservations: make(map[string]int),
		httpClient: &http.Client{
			Timeout: 6 * time.Second,
		},
		closeCh: make(chan struct{}),
	}
	c.placement = NewPlacementEngine(c)

	for _, wt := range cfg.Workers {
		c.workerTargets[wt.ID] = wt
		c.workers[wt.ID] = &models.WorkerInfo{
			ID:      wt.ID,
			URL:     wt.URL,
			Healthy: true,
			Resources: models.HostResources{
				WorkerID: wt.ID,
				Healthy:  true,
			},
		}
	}

	// Recover saved VM state from SQLite database
	c.recoverVMs()

	// Launch background health and VM state reconciliation loop
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.reconcilerLoop()
	}()

	return c
}

// Close gracefully stops background tasks and closes database.
func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		c.wg.Wait()
		if c.store != nil {
			_ = c.store.Close()
		}
	})
}

// getPendingReservation returns in-flight memory reservations for a worker.
func (c *Controller) getPendingReservation(workerID string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pendingReservations[workerID]
}

// addPendingReservation adds in-flight reservation before dispatch.
func (c *Controller) addPendingReservation(workerID string, memMB int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingReservations[workerID] += memMB
}

// releasePendingReservation removes in-flight reservation.
func (c *Controller) releasePendingReservation(workerID string, memMB int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingReservations[workerID] -= memMB
	if c.pendingReservations[workerID] < 0 {
		c.pendingReservations[workerID] = 0
	}
}

// GetWorkerInfos refreshes resource capacity from workers and returns their status.
func (c *Controller) GetWorkerInfos(ctx context.Context) []*models.WorkerInfo {
	c.mu.RLock()
	targets := make([]WorkerTarget, 0, len(c.workerTargets))
	for _, t := range c.workerTargets {
		targets = append(targets, t)
	}
	c.mu.RUnlock()

	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(t WorkerTarget) {
			defer wg.Done()
			c.pollWorkerResources(ctx, t)
		}(target)
	}
	wg.Wait()

	c.mu.RLock()
	defer c.mu.RUnlock()
	list := make([]*models.WorkerInfo, 0, len(c.workers))
	for _, w := range c.workers {
		cp := *w
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

func (c *Controller) pollWorkerResources(ctx context.Context, t WorkerTarget) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL+"/resources", nil)
	if err != nil {
		c.markWorkerFailure(t.ID)
		return
	}
	if t.Token != "" {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.markWorkerFailure(t.ID)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.markWorkerFailure(t.ID)
		return
	}

	var res models.HostResources
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		c.markWorkerFailure(t.ID)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if w, exists := c.workers[t.ID]; exists {
		w.Healthy = true
		w.ConsecutiveFails = 0
		w.LastHeartbeat = time.Now()
		w.Resources = res
	}
}

func (c *Controller) markWorkerFailure(workerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, exists := c.workers[workerID]; exists {
		w.ConsecutiveFails++
		if w.ConsecutiveFails >= 3 {
			w.Healthy = false
			w.Resources.Healthy = false
		}
	}
}

// CreateVM validates input, selects a host using memory placement, reserves memory,
// records state CREATING, dispatches to worker, and updates state.
func (c *Controller) CreateVM(ctx context.Context, req models.CreateVMRequest) (*models.VM, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	if req.ID == "" {
		req.ID = fmt.Sprintf("vm-%d", time.Now().UnixNano()%1000000)
	}

	c.mu.Lock()
	if _, exists := c.vms[req.ID]; exists {
		c.mu.Unlock()
		return nil, fmt.Errorf("vm with ID %s already exists", req.ID)
	}
	c.mu.Unlock()

	// Placement: select host with sufficient free memory and 512MB reserve
	targetWorker, err := c.placement.SelectHost(ctx, req)
	if err != nil {
		return nil, err
	}

	// Reservation before dispatch: lock in memory on chosen host
	c.addPendingReservation(targetWorker.ID, req.MemoryMB)
	defer c.releasePendingReservation(targetWorker.ID, req.MemoryMB)

	vm := &models.VM{
		ID:        req.ID,
		Name:      req.Name,
		VCPUs:     req.VCPUs,
		MemoryMB:  req.MemoryMB,
		State:     models.StateCreating,
		HostID:    targetWorker.ID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	c.mu.Lock()
	c.vms[vm.ID] = vm
	c.saveVMLocked(vm)
	c.mu.Unlock()

	// Dispatch create to worker with bounded retries
	var createdVM *models.VM
	var createErr error
	workerURL := targetWorker.URL
	workerToken := c.workerTargets[targetWorker.ID].Token

	for attempt := 0; attempt < 2; attempt++ {
		createdVM, createErr = c.workerCreateCall(ctx, workerURL, workerToken, req)
		if createErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if createErr != nil {
		// Roll back and mark as ERROR
		c.mu.Lock()
		vm.State = models.StateError
		vm.ErrorMessage = "worker create failed: " + createErr.Error()
		vm.UpdatedAt = time.Now()
		c.saveVMLocked(vm)
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to create VM on worker %s: %w", targetWorker.ID, createErr)
	}

	c.mu.Lock()
	vm.State = createdVM.State
	if vm.State == "" {
		vm.State = models.StateCreated
	}
	vm.IPAddress = createdVM.IPAddress
	vm.OS = createdVM.OS
	vm.SSHPort = createdVM.SSHPort
	vm.SerialSocket = createdVM.SerialSocket
	vm.UpdatedAt = time.Now()
	c.saveVMLocked(vm)
	c.mu.Unlock()

	return vm, nil
}

func (c *Controller) workerCreateCall(ctx context.Context, url, token string, req models.CreateVMRequest) (*models.VM, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/vms", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var apiResp models.APIResponse
		_ = json.NewDecoder(resp.Body).Decode(&apiResp)
		if apiResp.Error != "" {
			return nil, errors.New(apiResp.Error)
		}
		return nil, fmt.Errorf("worker returned HTTP %d", resp.StatusCode)
	}

	var apiResp struct {
		Success bool       `json:"success"`
		Data    *models.VM `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	return apiResp.Data, nil
}

// StartVM initiates execution of a created or stopped VM.
func (c *Controller) StartVM(ctx context.Context, vmID string) (*models.VM, error) {
	c.mu.Lock()
	vm, exists := c.vms[vmID]
	if !exists {
		c.mu.Unlock()
		return nil, ErrVMNotFound
	}

	if vm.State == models.StateRunning {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: cannot start VM already in RUNNING state", models.ErrInvalidTransition)
	}

	if !models.IsValidTransition(vm.State, models.StateRunning) {
		state := vm.State
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: cannot transition from %s to RUNNING", models.ErrInvalidTransition, state)
	}

	hostID := vm.HostID
	target := c.workerTargets[hostID]
	c.mu.Unlock()

	// Call worker to start
	url := fmt.Sprintf("%s/vms/%s/start", target.URL, vmID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	if target.Token != "" {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.mu.Lock()
		vm.State = models.StateError
		vm.ErrorMessage = "start failed: " + err.Error()
		vm.UpdatedAt = time.Now()
		c.mu.Unlock()
		return nil, fmt.Errorf("worker call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiResp models.APIResponse
		_ = json.NewDecoder(resp.Body).Decode(&apiResp)
		c.mu.Lock()
		vm.State = models.StateError
		vm.ErrorMessage = apiResp.Error
		vm.UpdatedAt = time.Now()
		c.saveVMLocked(vm)
		c.mu.Unlock()
		return nil, fmt.Errorf("worker failed to start VM: %s", apiResp.Error)
	}

	c.mu.Lock()
	vm.State = models.StateRunning
	vm.ErrorMessage = ""
	vm.UpdatedAt = time.Now()
	c.saveVMLocked(vm)
	copyVM := *vm
	c.mu.Unlock()

	return &copyVM, nil
}

// StopVM stops a running VM (idempotent).
func (c *Controller) StopVM(ctx context.Context, vmID string) (*models.VM, error) {
	c.mu.Lock()
	vm, exists := c.vms[vmID]
	if !exists {
		c.mu.Unlock()
		return nil, ErrVMNotFound
	}

	// Idempotent stop: if already STOPPED, return success
	if vm.State == models.StateStopped {
		copyVM := *vm
		c.mu.Unlock()
		return &copyVM, nil
	}

	if !models.IsValidTransition(vm.State, models.StateStopped) {
		state := vm.State
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: cannot transition from %s to STOPPED", models.ErrInvalidTransition, state)
	}

	hostID := vm.HostID
	target := c.workerTargets[hostID]
	c.mu.Unlock()

	url := fmt.Sprintf("%s/vms/%s/stop", target.URL, vmID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	if target.Token != "" {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker stop call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiResp models.APIResponse
		_ = json.NewDecoder(resp.Body).Decode(&apiResp)
		return nil, fmt.Errorf("worker stop failed: %s", apiResp.Error)
	}

	c.mu.Lock()
	vm.State = models.StateStopped
	vm.UpdatedAt = time.Now()
	c.saveVMLocked(vm)
	copyVM := *vm
	c.mu.Unlock()

	return &copyVM, nil
}

// DeleteVM cleans up a VM on its worker and marks it DELETED.
func (c *Controller) DeleteVM(ctx context.Context, vmID string) error {
	c.mu.Lock()
	vm, exists := c.vms[vmID]
	if !exists {
		c.mu.Unlock()
		return ErrVMNotFound
	}

	// Cannot delete a RUNNING VM without stopping first
	if vm.State == models.StateRunning {
		c.mu.Unlock()
		return fmt.Errorf("%w: must stop VM before deleting", models.ErrInvalidTransition)
	}

	hostID := vm.HostID
	target := c.workerTargets[hostID]
	c.mu.Unlock()

	url := fmt.Sprintf("%s/vms/%s", target.URL, vmID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if target.Token != "" {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[Controller] Warning: worker delete call failed: %v", err)
	} else {
		_ = resp.Body.Close()
	}

	c.mu.Lock()
	delete(c.vms, vmID)
	c.deleteVMLocked(vmID)
	c.mu.Unlock()

	return nil
}

// saveVMLocked persists a VM record to SQLite. Must be called with c.mu held.
func (c *Controller) saveVMLocked(vm *models.VM) {
	if c.store == nil || vm == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.store.SaveVM(ctx, vm); err != nil {
		log.Printf("[Controller] Error saving VM %s to sqlite: %v", vm.ID, err)
	}
}

// deleteVMLocked removes a VM record from SQLite. Must be called with c.mu held.
func (c *Controller) deleteVMLocked(vmID string) {
	if c.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.store.DeleteVM(ctx, vmID); err != nil {
		log.Printf("[Controller] Error deleting VM %s from sqlite: %v", vmID, err)
	}
}

// recoverVMs restores VM registry from SQLite database on startup.
func (c *Controller) recoverVMs() {
	if c.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vms, err := c.store.ListVMs(ctx)
	if err != nil {
		log.Printf("[Controller] Error recovering VMs from sqlite: %v", err)
		return
	}

	c.mu.Lock()
	for _, vm := range vms {
		c.vms[vm.ID] = vm
	}
	c.mu.Unlock()
	log.Printf("[Controller] Recovered %d VM(s) from SQLite database", len(vms))
}

// GetVM retrieves a single VM by ID.
func (c *Controller) GetVM(vmID string) (*models.VM, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	vm, exists := c.vms[vmID]
	if !exists {
		return nil, ErrVMNotFound
	}
	copyVM := *vm
	return &copyVM, nil
}

// ListVMs retrieves all active VMs sorted deterministically.
func (c *Controller) ListVMs() []*models.VM {
	c.mu.RLock()
	defer c.mu.RUnlock()

	res := make([]*models.VM, 0, len(c.vms))
	for _, vm := range c.vms {
		copyVM := *vm
		res = append(res, &copyVM)
	}

	sort.Slice(res, func(i, j int) bool {
		if res[i].CreatedAt.Equal(res[j].CreatedAt) {
			return res[i].ID < res[j].ID
		}
		return res[i].CreatedAt.Before(res[j].CreatedAt)
	})

	return res
}
