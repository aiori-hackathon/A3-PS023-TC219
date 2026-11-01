package worker

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"vm-controller/src/pkg/models"

	"github.com/creack/pty"
)

// MockVMInstance simulates an active VM instance.
type MockVMInstance struct {
	VM             models.VM
	State          models.VMState
	Dir            string
	Socket         string
	DiskFile       string
	PID            int
	SimCrash       bool
	SerialListener net.Listener
	SerialStop     chan struct{}
}

// MockHypervisor simulates hypervisor operations for prototyping, development, and testing.
type MockHypervisor struct {
	mu        sync.RWMutex
	baseDir   string
	instances map[string]*MockVMInstance
	pidSeq    int
}

// NewMockHypervisor creates a new MockHypervisor instance.
func NewMockHypervisor(baseDir string) *MockHypervisor {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "mock-hypervisor")
	}
	_ = os.MkdirAll(baseDir, 0755)
	return &MockHypervisor{
		baseDir:   baseDir,
		instances: make(map[string]*MockVMInstance),
		pidSeq:    1000,
	}
}

// CreateVM allocates mock disk and socket, and initializes the VM in CREATED state.
func (m *MockHypervisor) CreateVM(ctx context.Context, vm *models.VM) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Error injection test hook: name contains "inject-fail-create"
	if strings.Contains(vm.Name, "inject-fail-create") {
		return fmt.Errorf("injected hypervisor create failure for %s", vm.Name)
	}

	vmDir := filepath.Join(m.baseDir, vm.ID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("failed to create vm dir: %w", err)
	}

	sockPath := filepath.Join(vmDir, "ch.sock")
	diskPath := filepath.Join(vmDir, "disk.raw")
	serialPath := filepath.Join(vmDir, "serial.sock")

	// Create dummy files representing socket and disk
	if err := os.WriteFile(sockPath, []byte("mock-socket"), 0600); err != nil {
		_ = os.RemoveAll(vmDir)
		return fmt.Errorf("failed to create mock socket: %w", err)
	}
	if err := os.WriteFile(diskPath, []byte("mock-disk-image"), 0600); err != nil {
		_ = os.RemoveAll(vmDir)
		return fmt.Errorf("failed to create mock disk: %w", err)
	}
	if err := os.WriteFile(serialPath, []byte("mock-serial"), 0600); err != nil {
		_ = os.RemoveAll(vmDir)
		return fmt.Errorf("failed to create mock serial: %w", err)
	}

	welcomeMsg := fmt.Sprintf("===============================================================\n"+
		" Welcome to %s (VM ID: %s)\n"+
		" Guest OS: %s | vCPUs: %d | Memory: %dMB\n"+
		" Serial Console: ttyS0\n"+
		" Type real OS commands: ls, uname, cat, touch, top, etc.\n"+
		"===============================================================\n",
		vm.Name, vm.ID, vm.OS, vm.VCPUs, vm.MemoryMB)
	_ = os.WriteFile(filepath.Join(vmDir, "welcome.txt"), []byte(welcomeMsg), 0644)

	vm.SerialSocket = serialPath
	m.pidSeq++
	m.instances[vm.ID] = &MockVMInstance{
		VM:       *vm,
		State:    models.StateCreated,
		Dir:      vmDir,
		Socket:   sockPath,
		DiskFile: diskPath,
		PID:      m.pidSeq,
	}

	return nil
}

// BootVM transitions the VM to RUNNING and launches simulated serial console.
func (m *MockHypervisor) BootVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[vmID]
	if !exists {
		return fmt.Errorf("vm %s not found in hypervisor", vmID)
	}

	// Error injection test hook: name contains "inject-fail-boot"
	if strings.Contains(inst.VM.Name, "inject-fail-boot") {
		inst.State = models.StateError
		return fmt.Errorf("injected hypervisor boot failure for %s", inst.VM.Name)
	}

	inst.State = models.StateRunning
	m.startSerialConsoleLocked(inst)
	return nil
}

// StopVM stops the VM and transitions to STOPPED.
func (m *MockHypervisor) StopVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[vmID]
	if !exists {
		return fmt.Errorf("vm %s not found in hypervisor", vmID)
	}

	m.stopSerialConsoleLocked(inst)

	// Idempotent: already stopped is successful
	if inst.State == models.StateStopped {
		return nil
	}

	inst.State = models.StateStopped
	return nil
}

// DeleteVM cleans up all resources for the VM.
func (m *MockHypervisor) DeleteVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[vmID]
	if exists {
		m.stopSerialConsoleLocked(inst)
		_ = os.RemoveAll(inst.Dir)
		delete(m.instances, vmID)
	} else {
		// Even if not tracked, clean directory if it exists
		vmDir := filepath.Join(m.baseDir, vmID)
		_ = os.Remove(filepath.Join(vmDir, "serial.sock"))
		_ = os.RemoveAll(vmDir)
	}

	return nil
}

// GetVMStatus inspects current VM status.
func (m *MockHypervisor) GetVMStatus(ctx context.Context, vmID string) (models.VMState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[vmID]
	if !exists {
		return "", fmt.Errorf("vm %s not found", vmID)
	}

	if inst.SimCrash {
		return models.StateError, fmt.Errorf("vm process crashed unexpectedly")
	}

	return inst.State, nil
}

// SimulateCrash sets simulated crash flag for testing recovery.
func (m *MockHypervisor) SimulateCrash(vmID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[vmID]; ok {
		inst.SimCrash = true
		inst.State = models.StateError
	}
}

// CleanupOrphans scans the base directory and removes orphaned files for deleted VMs.
func (m *MockHypervisor) CleanupOrphans(ctx context.Context, activeVMIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	activeMap := make(map[string]bool)
	for _, id := range activeVMIDs {
		activeMap[id] = true
	}

	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			vmID := entry.Name()
			if !activeMap[vmID] {
				// Orphan directory found! Clean it up.
				orphanDir := filepath.Join(m.baseDir, vmID)
				_ = os.RemoveAll(orphanDir)
				delete(m.instances, vmID)
			}
		}
	}
	return nil
}

// RecoverVM re-attaches an existing VM to the mock hypervisor.
func (m *MockHypervisor) RecoverVM(ctx context.Context, vm *models.VM) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vmDir := filepath.Join(m.baseDir, vm.ID)
	sockPath := filepath.Join(vmDir, "ch.sock")
	diskPath := filepath.Join(vmDir, "disk.raw")

	inst := &MockVMInstance{
		VM:       *vm,
		State:    vm.State,
		Dir:      vmDir,
		Socket:   sockPath,
		DiskFile: diskPath,
		PID:      1000,
	}
	m.instances[vm.ID] = inst

	if vm.State == models.StateRunning {
		m.startSerialConsoleLocked(inst)
	}

	return nil
}

// GetVMResourceUsage returns real-time resource utilization (CPU, Memory) for a mock VM.
func (m *MockHypervisor) GetVMResourceUsage(ctx context.Context, vmID string) (*models.VMResourceUsage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[vmID]
	if !exists {
		return nil, fmt.Errorf("vm %s not found in hypervisor", vmID)
	}

	if inst.State != models.StateRunning {
		return &models.VMResourceUsage{
			CPUPercent:    0,
			MemoryUsedMB:  0,
			MemoryLimitMB: inst.VM.MemoryMB,
			MemoryPercent: 0,
		}, nil
	}

	// Active running VM: simulate realistic memory footprint (~25% of VM capacity) and low idle/active CPU
	usedMB := int(float64(inst.VM.MemoryMB) * 0.28)
	if usedMB < 32 {
		usedMB = 32
	}
	pct := math.Round((float64(usedMB)/float64(inst.VM.MemoryMB))*1000) / 10

	return &models.VMResourceUsage{
		CPUPercent:    1.4,
		MemoryUsedMB:  usedMB,
		MemoryLimitMB: inst.VM.MemoryMB,
		MemoryPercent: pct,
	}, nil
}

// Close stops all active serial console listeners.
func (m *MockHypervisor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		m.stopSerialConsoleLocked(inst)
	}
	return nil
}

func (m *MockHypervisor) startSerialConsoleLocked(inst *MockVMInstance) {
	if inst.SerialListener != nil {
		return
	}
	sockPath := inst.VM.SerialSocket
	if sockPath == "" {
		sockPath = filepath.Join(inst.Dir, "serial.sock")
		inst.VM.SerialSocket = sockPath
	}
	_ = os.Remove(sockPath)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return
	}
	inst.SerialListener = l
	inst.SerialStop = make(chan struct{})

	go func(vm models.VM, vmDir string, listener net.Listener, stopCh chan struct{}) {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					return
				}
			}
			go handleRealShellConnection(conn, vm, vmDir, stopCh)
		}
	}(inst.VM, inst.Dir, l, inst.SerialStop)
}

func (m *MockHypervisor) stopSerialConsoleLocked(inst *MockVMInstance) {
	if inst.SerialListener != nil {
		if inst.SerialStop != nil {
			close(inst.SerialStop)
			inst.SerialStop = nil
		}
		_ = inst.SerialListener.Close()
		inst.SerialListener = nil
	}
	if inst.VM.SerialSocket != "" {
		_ = os.Remove(inst.VM.SerialSocket)
	}
}

// handleRealShellConnection launches a genuine interactive OS shell on a pseudo-terminal (PTY)
// and connects it to the VM serial socket.
func handleRealShellConnection(conn net.Conn, vm models.VM, vmDir string, stopCh <-chan struct{}) {
	defer conn.Close()

	osName := vm.OS
	if osName == "" {
		osName = "Linux"
	}

	banner := fmt.Sprintf("\r\n\r\n"+
		"===============================================================\r\n"+
		" Virtual Machine: %s (%s) | %d vCPU(s) | %d MB Memory\r\n"+
		" Guest OS: %s | Serial Console: ttyS0\r\n"+
		" Interactive Shell Connected.\r\n"+
		"===============================================================\r\n\r\n",
		vm.ID, vm.Name, vm.VCPUs, vm.MemoryMB, osName)
	_, _ = conn.Write([]byte(banner))

	shell := "/bin/sh"
	if bashPath, err := exec.LookPath("bash"); err == nil {
		shell = bashPath
	} else if shPath, err := exec.LookPath("sh"); err == nil {
		shell = shPath
	}

	cmd := exec.Command(shell, "-i")
	cmd.Dir = vmDir
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color",
		"HOME=" + vmDir,
		"USER=root",
		"LOGNAME=root",
		"SHELL=" + shell,
		"PS1=root@vm-" + vm.ID + ":~# ",
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		// Fallback to stdio pipes if pseudo-terminal is unavailable
		cmd.Stdin = conn
		cmd.Stdout = conn
		cmd.Stderr = conn
		if err := cmd.Start(); err != nil {
			_, _ = conn.Write([]byte(fmt.Sprintf("Failed to spawn shell: %v\r\n", err)))
			return
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-stopCh:
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			case <-done:
			}
		}()
		_ = cmd.Wait()
		close(done)
		return
	}

	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(ptmx, conn)
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(conn, ptmx)
		done <- struct{}{}
	}()

	select {
	case <-stopCh:
	case <-done:
	}
}
