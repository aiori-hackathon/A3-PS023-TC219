package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"vm-controller/src/pkg/models"
	"vm-controller/src/pkg/store"

	"golang.org/x/crypto/ssh"
)

// Config configures a worker agent.
type Config struct {
	ID               string
	Token            string
	TotalMemoryMB    int
	ReservedMemoryMB int
	Driver           HypervisorDriver
	DataDir          string
	Store            store.Store
}

// Worker manages virtual machines on a single host.
type Worker struct {
	cfg              Config
	store            store.Store
	mu               sync.RWMutex
	vms              map[string]*models.VM
	allocatedIPs     map[string]bool
	allocatedPorts   map[int]bool
	sshBridges       map[string]*SSHBridge
	sshSigner        ssh.Signer
	consecutiveFails map[string]int
	driver           HypervisorDriver
	closeCh          chan struct{}
	closeOnce        sync.Once
	wg               sync.WaitGroup
}

// NewWorker initializes a new worker agent.
func NewWorker(cfg Config) *Worker {
	if cfg.ReservedMemoryMB <= 0 {
		cfg.ReservedMemoryMB = 512
	}
	if cfg.TotalMemoryMB <= 0 {
		cfg.TotalMemoryMB = 4096
	}
	if cfg.DataDir == "" && cfg.Store == nil {
		cfg.DataDir = filepath.Join(os.TempDir(), "vm-worker-"+cfg.ID)
	}
	if cfg.DataDir != "" {
		_ = os.MkdirAll(cfg.DataDir, 0755)
	}

	var s store.Store = cfg.Store
	if s == nil && cfg.DataDir != "" {
		dbPath := filepath.Join(cfg.DataDir, "worker.db")
		sqliteStore, err := store.NewSQLiteStore(dbPath)
		if err != nil {
			log.Printf("[Worker %s] Warning: failed to initialize SQLite database at %s: %v", cfg.ID, dbPath, err)
		} else {
			s = sqliteStore
		}
	}

	signer, _ := generateHostKey()
	w := &Worker{
		cfg:              cfg,
		store:            s,
		vms:              make(map[string]*models.VM),
		allocatedIPs:     make(map[string]bool),
		allocatedPorts:   make(map[int]bool),
		sshBridges:       make(map[string]*SSHBridge),
		sshSigner:        signer,
		consecutiveFails: make(map[string]int),
		driver:           cfg.Driver,
		closeCh:          make(chan struct{}),
	}

	// Recover existing VMs from SQLite before running reconciler
	w.recoverVMs()

	// Start background reconciler and orphan cleanup
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.reconcilerLoop()
	}()

	return w
}

func (w *Worker) saveVMLocked(vm *models.VM) {
	if w.store == nil || vm == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.store.SaveVM(ctx, vm); err != nil {
		log.Printf("[Worker %s] Error saving VM %s to sqlite: %v", w.cfg.ID, vm.ID, err)
	}
}

func (w *Worker) deleteVMLocked(vmID string) {
	if w.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.store.DeleteVM(ctx, vmID); err != nil {
		log.Printf("[Worker %s] Error deleting VM %s from sqlite: %v", w.cfg.ID, vmID, err)
	}
}

func (w *Worker) recoverVMs() {
	if w.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vms, err := w.store.ListVMs(ctx)
	if err != nil {
		log.Printf("[Worker %s] Failed to list VMs from sqlite: %v", w.cfg.ID, err)
		return
	}

	for _, vm := range vms {
		// Re-attach VM in driver
		if err := w.driver.RecoverVM(ctx, vm); err != nil {
			log.Printf("[Worker %s] Failed to recover VM %s in driver: %v", w.cfg.ID, vm.ID, err)
		}

		// Check status from hypervisor
		status, err := w.driver.GetVMStatus(ctx, vm.ID)
		if err == nil && status == models.StateRunning {
			vm.State = models.StateRunning
			w.allocatedIPs[vm.IPAddress] = true

			// Restore SSH Bridge
			if vm.SSHPort > 0 && vm.SerialSocket != "" {
				if _, err := os.Stat(vm.SerialSocket); err == nil {
					bridge, err := StartSSHBridge(vm.SerialSocket, vm.SSHPort, w.sshSigner)
					if err == nil {
						w.sshBridges[vm.ID] = bridge
						w.allocatedPorts[vm.SSHPort] = true
						log.Printf("[Worker %s] Re-bound SSH bridge for recovered VM %s on port %d", w.cfg.ID, vm.ID, vm.SSHPort)
					} else {
						// Port might have been taken; allocate new port
						newPort := w.allocatePortLocked()
						bridge, err := StartSSHBridge(vm.SerialSocket, newPort, w.sshSigner)
						if err == nil {
							w.sshBridges[vm.ID] = bridge
							w.allocatedPorts[newPort] = true
							vm.SSHPort = newPort
							log.Printf("[Worker %s] Re-bound SSH bridge for recovered VM %s on new port %d", w.cfg.ID, vm.ID, newPort)
						}
					}
				}
			}
		} else if vm.State == models.StateRunning {
			// Hypervisor was running before restart, but process not alive now
			vm.State = models.StateError
			vm.ErrorMessage = "hypervisor process terminated while worker was offline"
			vm.SSHPort = 0
		} else {
			// Created or Stopped state
			w.allocatedIPs[vm.IPAddress] = true
		}

		w.vms[vm.ID] = vm
		w.saveVMLocked(vm)
		log.Printf("[Worker %s] Recovered VM %s (%s) in state %s from SQLite", w.cfg.ID, vm.ID, vm.Name, vm.State)
	}
}

func (w *Worker) allocatePortLocked() int {
	base := 2200
	if strings.Contains(w.cfg.ID, "2") {
		base = 2300
	}
	for p := base + 1; p < base + 100; p++ {
		if !w.allocatedPorts[p] {
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
			if err == nil {
				_ = l.Close()
				w.allocatedPorts[p] = true
				return p
			}
		}
	}
	return base + 1
}

func (w *Worker) releasePortLocked(port int) {
	delete(w.allocatedPorts, port)
}

func (w *Worker) allocateIPLocked() string {
	subnet := 100
	if strings.Contains(w.cfg.ID, "2") {
		subnet = 200
	}
	for i := 10; i < 250; i++ {
		ip := fmt.Sprintf("192.168.%d.%d", subnet, i)
		if !w.allocatedIPs[ip] {
			w.allocatedIPs[ip] = true
			return ip
		}
	}
	return fmt.Sprintf("192.168.%d.10", subnet)
}

func (w *Worker) releaseIPLocked(ip string) {
	delete(w.allocatedIPs, ip)
}

// Close stops background tasks and releases resources.
func (w *Worker) Close() {
	w.closeOnce.Do(func() {
		close(w.closeCh)
		w.wg.Wait()
		w.mu.Lock()
		for id, bridge := range w.sshBridges {
			bridge.Close()
			delete(w.sshBridges, id)
		}
		w.mu.Unlock()
		if closer, ok := w.driver.(io.Closer); ok {
			_ = closer.Close()
		}
		if w.store != nil {
			_ = w.store.Close()
		}
	})
}

// GetResources calculates and returns current host resource capacity.
func (w *Worker) GetResources() models.HostResources {
	w.mu.RLock()
	defer w.mu.RUnlock()

	runningVMs := 0
	usedMemory := 0
	vmStates := make(map[string]models.VMState)

	for id, vm := range w.vms {
		vmStates[id] = vm.State
		if vm.State == models.StateRunning || vm.State == models.StateCreating {
			runningVMs++
			usedMemory += vm.MemoryMB
		}
	}

	availableMem := w.cfg.TotalMemoryMB - usedMemory
	if availableMem < 0 {
		availableMem = 0
	}

	freeMem := availableMem - w.cfg.ReservedMemoryMB
	if freeMem < 0 {
		freeMem = 0
	}

	return models.HostResources{
		WorkerID:          w.cfg.ID,
		Healthy:           true,
		TotalMemoryMB:     w.cfg.TotalMemoryMB,
		AvailableMemoryMB: availableMem,
		FreeMemoryMB:      freeMem,
		ReservedMemoryMB:  w.cfg.ReservedMemoryMB,
		RunningVMs:        runningVMs,
		TotalVMs:          len(w.vms),
		VMs:               vmStates,
	}
}

// CreateVM handles local VM creation, allocation, and optional boot.
func (w *Worker) CreateVM(ctx context.Context, req models.CreateVMRequest) (*models.VM, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	w.mu.Lock()
	if _, exists := w.vms[req.ID]; exists {
		w.mu.Unlock()
		return nil, fmt.Errorf("vm with id %s already exists", req.ID)
	}

	// Calculate if memory is available (respecting 512MB host reserve)
	usedMemory := 0
	for _, v := range w.vms {
		if v.State == models.StateRunning || v.State == models.StateCreating {
			usedMemory += v.MemoryMB
		}
	}
	available := w.cfg.TotalMemoryMB - usedMemory
	if available-w.cfg.ReservedMemoryMB < req.MemoryMB {
		w.mu.Unlock()
		return nil, fmt.Errorf("insufficient memory on worker: requires %d MB + %d MB reserve, but available is %d MB",
			req.MemoryMB, w.cfg.ReservedMemoryMB, available)
	}

	osName := req.OS
	if osName == "" {
		osName = "MINIX 3"
	}
	allocatedIP := "127.0.0.1"

	vm := &models.VM{
		ID:        req.ID,
		Name:      req.Name,
		VCPUs:     req.VCPUs,
		MemoryMB:  req.MemoryMB,
		OS:        osName,
		IPAddress: allocatedIP,
		SSHPort:   0,
		State:     models.StateCreating,
		HostID:    w.cfg.ID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	w.vms[vm.ID] = vm
	w.mu.Unlock()

	// Invoke hypervisor driver to configure VM
	if err := w.driver.CreateVM(ctx, vm); err != nil {
		// Roll back and cleanup
		w.mu.Lock()
		delete(w.vms, vm.ID)
		w.mu.Unlock()
		_ = w.driver.DeleteVM(ctx, vm.ID)
		return nil, fmt.Errorf("hypervisor create failed: %w", err)
	}

	w.mu.Lock()
	vm.State = models.StateCreated
	vm.UpdatedAt = time.Now()
	w.saveVMLocked(vm)
	w.mu.Unlock()

	if req.AutoBoot {
		if err := w.BootVM(ctx, vm.ID); err != nil {
			// Roll back and clean up on boot failure
			_ = w.DeleteVM(ctx, vm.ID)
			return nil, fmt.Errorf("autoboot failed: %w", err)
		}
	}

	return vm, nil
}

// BootVM starts an existing VM and launches the SSH bridge to its serial console.
func (w *Worker) BootVM(ctx context.Context, vmID string) error {
	w.mu.Lock()
	vm, exists := w.vms[vmID]
	if !exists {
		w.mu.Unlock()
		return fmt.Errorf("vm %s not found", vmID)
	}

	if vm.State == models.StateRunning {
		w.mu.Unlock()
		return fmt.Errorf("%w: vm is already running", models.ErrInvalidTransition)
	}

	if !models.IsValidTransition(vm.State, models.StateRunning) {
		state := vm.State
		w.mu.Unlock()
		return fmt.Errorf("%w: cannot transition from %s to RUNNING", models.ErrInvalidTransition, state)
	}
	w.mu.Unlock()

	if err := w.driver.BootVM(ctx, vmID); err != nil {
		w.mu.Lock()
		vm.State = models.StateError
		vm.ErrorMessage = err.Error()
		vm.UpdatedAt = time.Now()
		w.mu.Unlock()
		return fmt.Errorf("failed to boot vm: %w", err)
	}

	w.mu.Lock()
	vm.State = models.StateRunning
	vm.ErrorMessage = ""
	vm.UpdatedAt = time.Now()

	// Start SSH bridge if serial console socket exists
	if vm.SerialSocket != "" {
		serialSock := vm.SerialSocket
		w.mu.Unlock()
		for i := 0; i < 10; i++ {
			if _, err := os.Stat(serialSock); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		w.mu.Lock()
		if _, err := os.Stat(serialSock); err == nil {
			if _, exists := w.sshBridges[vm.ID]; !exists {
				port := w.allocatePortLocked()
				bridge, err := StartSSHBridge(serialSock, port, w.sshSigner)
				if err == nil {
					w.sshBridges[vm.ID] = bridge
					vm.SSHPort = port
					vm.IPAddress = "127.0.0.1"
					log.Printf("[Worker %s] Started SSH bridge for VM %s on 127.0.0.1:%d", w.cfg.ID, vm.ID, port)
				} else {
					log.Printf("[Worker %s] Failed to start SSH bridge for VM %s: %v", w.cfg.ID, vm.ID, err)
					w.releasePortLocked(port)
				}
			}
		}
	}
	w.saveVMLocked(vm)
	w.mu.Unlock()

	return nil
}

// StopVM stops a running VM (idempotent) and shuts down its SSH bridge.
func (w *Worker) StopVM(ctx context.Context, vmID string) error {
	w.mu.Lock()
	vm, exists := w.vms[vmID]
	if !exists {
		w.mu.Unlock()
		return fmt.Errorf("vm %s not found", vmID)
	}

	// Idempotency: if already STOPPED, return success
	if vm.State == models.StateStopped {
		w.mu.Unlock()
		return nil
	}

	if !models.IsValidTransition(vm.State, models.StateStopped) {
		state := vm.State
		w.mu.Unlock()
		return fmt.Errorf("%w: cannot transition from %s to STOPPED", models.ErrInvalidTransition, state)
	}
	w.mu.Unlock()

	if err := w.driver.StopVM(ctx, vmID); err != nil {
		return fmt.Errorf("hypervisor stop failed: %w", err)
	}

	w.mu.Lock()
	if bridge, ok := w.sshBridges[vmID]; ok {
		bridge.Close()
		delete(w.sshBridges, vmID)
		w.releasePortLocked(vm.SSHPort)
	}
	vm.SSHPort = 0
	vm.State = models.StateStopped
	vm.UpdatedAt = time.Now()
	w.saveVMLocked(vm)
	w.mu.Unlock()

	return nil
}

// DeleteVM tears down hypervisor resources and deletes local record.
func (w *Worker) DeleteVM(ctx context.Context, vmID string) error {
	w.mu.Lock()
	vm, exists := w.vms[vmID]
	if !exists {
		w.mu.Unlock()
		// Even if not in map, make sure any orphan files are cleaned
		_ = w.driver.DeleteVM(ctx, vmID)
		w.deleteVMLocked(vmID)
		return nil
	}
	if bridge, ok := w.sshBridges[vmID]; ok {
		bridge.Close()
		delete(w.sshBridges, vmID)
		w.releasePortLocked(vm.SSHPort)
	}
	delete(w.vms, vmID)
	delete(w.consecutiveFails, vmID)
	w.releaseIPLocked(vm.IPAddress)
	w.deleteVMLocked(vmID)
	w.mu.Unlock()

	if err := w.driver.DeleteVM(ctx, vmID); err != nil {
		return fmt.Errorf("failed to delete hypervisor resources: %w", err)
	}

	vm.State = models.StateDeleted
	return nil
}

// GetVM returns a single VM by ID.
func (w *Worker) GetVM(vmID string) (*models.VM, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	vm, exists := w.vms[vmID]
	if !exists {
		return nil, fmt.Errorf("vm %s not found", vmID)
	}

	copyVM := *vm
	if usage, err := w.driver.GetVMResourceUsage(context.Background(), vmID); err == nil && usage != nil {
		copyVM.ResourceUsage = usage
	}
	return &copyVM, nil
}

// ListVMs returns all tracked VMs on this worker sorted deterministically.
func (w *Worker) ListVMs() []*models.VM {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make([]*models.VM, 0, len(w.vms))
	for _, vm := range w.vms {
		copyVM := *vm
		if usage, err := w.driver.GetVMResourceUsage(context.Background(), vm.ID); err == nil && usage != nil {
			copyVM.ResourceUsage = usage
		}
		result = append(result, &copyVM)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result
}

// reconcilerLoop periodically checks VM process health, cleans up orphans, and handles recovery.
func (w *Worker) reconcilerLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.closeCh:
			return
		case <-ticker.C:
			w.reconcile()
		}
	}
}

func (w *Worker) reconcile() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	w.mu.Lock()
	activeIDs := make([]string, 0, len(w.vms))
	for id, vm := range w.vms {
		activeIDs = append(activeIDs, id)

		// If supposed to be running, verify hypervisor status
		if vm.State == models.StateRunning {
			status, err := w.driver.GetVMStatus(ctx, id)
			if err != nil || status == models.StateError {
				w.consecutiveFails[id]++
				if w.consecutiveFails[id] >= 3 {
					log.Printf("[Worker %s] VM %s crashed or lost socket connection: %v. Recovering to ERROR state.", w.cfg.ID, id, err)
					if bridge, ok := w.sshBridges[id]; ok {
						bridge.Close()
						delete(w.sshBridges, id)
						w.releasePortLocked(vm.SSHPort)
						vm.SSHPort = 0
					}
					vm.State = models.StateError
					if err != nil {
						vm.ErrorMessage = err.Error()
					} else {
						vm.ErrorMessage = "hypervisor process terminated unexpectedly"
					}
					vm.UpdatedAt = time.Now()
					w.saveVMLocked(vm)
				}
			} else {
				w.consecutiveFails[id] = 0
			}
		}
	}
	w.mu.Unlock()

	// Clean up any orphaned files / sockets
	_ = w.driver.CleanupOrphans(ctx, activeIDs)
}
