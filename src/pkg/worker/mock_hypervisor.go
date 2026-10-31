package worker

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"vm-controller/src/pkg/models"
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

	go func(vm models.VM, listener net.Listener, stopCh chan struct{}) {
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
			go handleMockSerialConnection(conn, vm, stopCh)
		}
	}(inst.VM, l, inst.SerialStop)
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

func handleMockSerialConnection(conn net.Conn, vm models.VM, stopCh <-chan struct{}) {
	defer conn.Close()

	banner := fmt.Sprintf("\r\n"+
		"===============================================================\r\n"+
		" Welcome to MINIX 3 (x86_64) - Cloud Hypervisor Virtual Machine\r\n"+
		" VM: %s | Name: %s | vCPUs: %d | Memory: %dMB\r\n"+
		" System State: RUNNING | Console: ttyS0\r\n"+
		" Type 'help' for commands, or 'exit' to disconnect.\r\n"+
		"===============================================================\r\n\r\n"+
		"minix# ", vm.ID, vm.Name, vm.VCPUs, vm.MemoryMB)

	_, _ = conn.Write([]byte(banner))

	buf := make([]byte, 1024)
	var lineBuf []byte
	lastWasCR := false

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		for i := 0; i < n; i++ {
			b := buf[i]

			if b == '\r' {
				lastWasCR = true
				_, _ = conn.Write([]byte("\r\n"))
				executeMockCommand(conn, vm, string(lineBuf))
				lineBuf = nil
				_, _ = conn.Write([]byte("minix# "))
			} else if b == '\n' {
				if lastWasCR {
					lastWasCR = false
					continue
				}
				_, _ = conn.Write([]byte("\r\n"))
				executeMockCommand(conn, vm, string(lineBuf))
				lineBuf = nil
				_, _ = conn.Write([]byte("minix# "))
			} else {
				lastWasCR = false
				switch b {
				case 0x7f, 0x08: // Backspace
					if len(lineBuf) > 0 {
						lineBuf = lineBuf[:len(lineBuf)-1]
						_, _ = conn.Write([]byte("\b \b"))
					}
				case 0x03: // Ctrl+C
					lineBuf = nil
					_, _ = conn.Write([]byte("^C\r\nminix# "))
				case 0x04: // Ctrl+D (EOF)
					if len(lineBuf) == 0 {
						_, _ = conn.Write([]byte("\r\nLogout.\r\n"))
						return
					}
				default:
					if b >= 32 && b <= 126 {
						lineBuf = append(lineBuf, b)
						_, _ = conn.Write([]byte{b})
					}
				}
			}
		}
	}
}

func executeMockCommand(conn net.Conn, vm models.VM, rawCmd string) {
	cmd := strings.TrimSpace(rawCmd)
	if cmd == "" {
		return
	}

	parts := strings.Fields(cmd)
	rootCmd := parts[0]

	switch rootCmd {
	case "help":
		helpText := "Available commands:\r\n" +
			"  help          - Show this help menu\r\n" +
			"  uname [-a]    - Print system and kernel information\r\n" +
			"  whoami        - Print current user\r\n" +
			"  uptime        - Print system uptime and load\r\n" +
			"  free [-m]     - Print memory utilization\r\n" +
			"  ps [aux]      - Print active process table\r\n" +
			"  cat <file>    - Display simulated file (/etc/os-release, /etc/issue)\r\n" +
			"  date          - Show current system date and time\r\n" +
			"  echo <text>   - Echo text back to console\r\n" +
			"  clear         - Clear terminal screen\r\n" +
			"  exit, quit    - Close console session\r\n"
		_, _ = conn.Write([]byte(helpText))

	case "uname":
		_, _ = conn.Write([]byte("Minix localhost 3.4.0 #1 SMP x86_64\r\n"))

	case "whoami", "id":
		_, _ = conn.Write([]byte("root (uid=0 gid=0 groups=0(wheel))\r\n"))

	case "uptime":
		_, _ = conn.Write([]byte(fmt.Sprintf(" %s up 15 min,  1 user,  load average: 0.08, 0.02, 0.01\r\n", time.Now().Format("15:04:05"))))

	case "free":
		total := vm.MemoryMB
		used := int(float64(total) * 0.28)
		free := total - used
		freeText := fmt.Sprintf("              total        used        free      shared  buff/cache   available\r\n"+
			"Mem:        %7d     %7d     %7d           0           0     %7d\r\n"+
			"Swap:             0           0           0\r\n", total, used, free, free)
		_, _ = conn.Write([]byte(freeText))

	case "ps":
		psText := "  PID TTY          TIME CMD\r\n" +
			"    1 ttyS0    00:00:01 init\r\n" +
			"    2 ?        00:00:00 system\r\n" +
			"    3 ?        00:00:00 pm\r\n" +
			"    4 ?        00:00:00 fs\r\n" +
			"    5 ?        00:00:00 rs\r\n" +
			"    6 ?        00:00:00 ds\r\n" +
			"    7 ttyS0    00:00:00 tty\r\n" +
			"    8 ttyS0    00:00:00 sh\r\n"
		_, _ = conn.Write([]byte(psText))

	case "cat":
		if len(parts) > 1 && (parts[1] == "/etc/os-release" || parts[1] == "/etc/issue") {
			osRel := "NAME=\"MINIX 3\"\r\n" +
				"VERSION=\"3.4.0\"\r\n" +
				"ID=minix\r\n" +
				"PRETTY_NAME=\"MINIX 3.4.0 (x86_64)\"\r\n" +
				"HOME_URL=\"https://minix3.org/\"\r\n"
			_, _ = conn.Write([]byte(osRel))
		} else if len(parts) > 1 {
			_, _ = conn.Write([]byte(fmt.Sprintf("cat: %s: No such file or directory\r\n", parts[1])))
		} else {
			_, _ = conn.Write([]byte("cat: missing operand\r\n"))
		}

	case "echo":
		if len(parts) > 1 {
			text := strings.TrimPrefix(cmd, "echo ")
			_, _ = conn.Write([]byte(text + "\r\n"))
		} else {
			_, _ = conn.Write([]byte("\r\n"))
		}

	case "date":
		_, _ = conn.Write([]byte(time.Now().UTC().Format(time.UnixDate) + "\r\n"))

	case "clear":
		_, _ = conn.Write([]byte("\033[H\033[2J"))

	case "exit", "quit", "logout":
		_, _ = conn.Write([]byte("Logout.\r\n"))
		_ = conn.Close()

	default:
		_, _ = conn.Write([]byte(fmt.Sprintf("minix: %s: command not found\r\n", rootCmd)))
	}
}
