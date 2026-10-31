package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"vm-controller/src/pkg/models"
)

// CHVMInstance tracks an active Cloud Hypervisor process.
type CHVMInstance struct {
	VM           models.VM
	State        models.VMState
	Dir          string
	SockPath     string
	DiskPath     string
	Cmd          *exec.Cmd
	PGID         int
	lastCPUTicks uint64
	lastCPUTime  time.Time
	lastCPUUsage float64
}

// CloudHypervisorDriver manages real Cloud Hypervisor instances on a Linux KVM host.
type CloudHypervisorDriver struct {
	mu            sync.RWMutex
	baseDir       string
	binPath       string
	kernelPath    string
	baseDiskPath  string
	initramfsPath string
	instances     map[string]*CHVMInstance
}

func detectHostKernel() string {
	candidates := []string{
		"/boot/vmlinuz-6.14.0-37-generic",
		"/boot/vmlinuz",
	}
	for _, c := range candidates {
		if f, err := os.Open(c); err == nil {
			_ = f.Close()
			return c
		}
	}
	matches, _ := filepath.Glob("/boot/vmlinuz*")
	for _, m := range matches {
		if f, err := os.Open(m); err == nil {
			_ = f.Close()
			return m
		}
	}
	return ""
}

func ensureDefaultInitramfs(baseDir string) string {
	initrdPath := filepath.Join(baseDir, "guest-initramfs.cpio.gz")
	if _, err := os.Stat(initrdPath); err == nil {
		return initrdPath
	}
	busyboxPath, err := exec.LookPath("busybox")
	if err != nil {
		return ""
	}

	buildDir, err := os.MkdirTemp("", "initrd-build-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(buildDir)

	_ = os.MkdirAll(filepath.Join(buildDir, "bin"), 0755)
	_ = os.MkdirAll(filepath.Join(buildDir, "proc"), 0755)
	_ = os.MkdirAll(filepath.Join(buildDir, "sys"), 0755)
	_ = os.MkdirAll(filepath.Join(buildDir, "dev"), 0755)

	_ = copyFile(busyboxPath, filepath.Join(buildDir, "bin", "busybox"))
	_ = os.Chmod(filepath.Join(buildDir, "bin", "busybox"), 0755)

	initScript := `#!/bin/busybox sh
/bin/busybox --install -s /bin
mount -t devtmpfs devtmpfs /dev
mount -t proc proc /proc
mount -t sysfs sysfs /sys
echo ""
echo "=========================================================="
echo " Welcome to MINIX 3 / Microkernel Guest VM"
echo " Node: Cloud Hypervisor on Linux KVM"
echo " Interactive shell ready."
echo "=========================================================="
echo ""
exec setsid cttyhack sh
`
	_ = os.WriteFile(filepath.Join(buildDir, "init"), []byte(initScript), 0755)

	cmd := exec.Command("sh", "-c", fmt.Sprintf("cd %s && find . | cpio -o -H newc | gzip > %s", buildDir, initrdPath))
	if err := cmd.Run(); err == nil {
		return initrdPath
	}
	return ""
}

// NewCloudHypervisorDriver creates a new driver for managing Cloud Hypervisor.
func NewCloudHypervisorDriver(baseDir, binPath, kernelPath, baseDiskPath string) *CloudHypervisorDriver {
	if binPath == "" {
		binPath = "cloud-hypervisor"
	}
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "vm-worker")
	}
	_ = os.MkdirAll(baseDir, 0755)

	if kernelPath == "" {
		kernelPath = detectHostKernel()
	}

	initramfs := ensureDefaultInitramfs(baseDir)

	return &CloudHypervisorDriver{
		baseDir:       baseDir,
		binPath:       binPath,
		kernelPath:    kernelPath,
		baseDiskPath:  baseDiskPath,
		initramfsPath: initramfs,
		instances:     make(map[string]*CHVMInstance),
	}
}

// socketClient creates an HTTP client communicating over the VM's Unix domain socket.
func (d *CloudHypervisorDriver) socketClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

// CreateVM spawns a cloud-hypervisor process in its own process group and cgroup v2 scope,
// prepares disk, and configures the VM via its Unix socket.
func (d *CloudHypervisorDriver) CreateVM(ctx context.Context, vm *models.VM) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	vmDir := filepath.Join(d.baseDir, vm.ID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("failed to create vm dir: %w", err)
	}

	sockPath := filepath.Join(vmDir, "ch.sock")
	diskPath := filepath.Join(vmDir, "rootfs.raw")
	serialPath := filepath.Join(vmDir, "serial.sock")
	vm.SerialSocket = serialPath

	// If a base disk image is provided, copy it for this VM; otherwise create a sparse file.
	if d.baseDiskPath != "" {
		if err := copyFile(d.baseDiskPath, diskPath); err != nil {
			_ = os.RemoveAll(vmDir)
			return fmt.Errorf("failed to copy base disk: %w", err)
		}
	} else {
		// Create 100MB sparse file for VM disk
		f, err := os.Create(diskPath)
		if err != nil {
			_ = os.RemoveAll(vmDir)
			return fmt.Errorf("failed to create vm disk: %w", err)
		}
		_ = f.Truncate(100 * 1024 * 1024)
		_ = f.Close()
	}

	inst := &CHVMInstance{
		VM:       *vm,
		State:    models.StateCreated,
		Dir:      vmDir,
		SockPath: sockPath,
		DiskPath: diskPath,
	}

	if err := d.spawnProcess(ctx, inst); err != nil {
		_ = os.RemoveAll(vmDir)
		return err
	}

	d.instances[vm.ID] = inst
	return nil
}

// spawnProcess launches Cloud Hypervisor and sends vm.create payload.
func (d *CloudHypervisorDriver) spawnProcess(ctx context.Context, inst *CHVMInstance) error {
	_ = os.Remove(inst.SockPath)
	_ = os.Remove(inst.VM.SerialSocket)

	memoryBytes := uint64(inst.VM.MemoryMB) * 1024 * 1024
	cpuQuota := inst.VM.VCPUs * 100
	logPath := filepath.Join(inst.Dir, "cloud-hypervisor.log")

	var cmd *exec.Cmd
	if _, err := exec.LookPath("systemd-run"); err == nil {
		cmd = exec.Command(
			"systemd-run", "--user", "--scope",
			fmt.Sprintf("-pMemoryMax=%d", memoryBytes),
			fmt.Sprintf("-pCPUQuota=%d%%", cpuQuota),
			d.binPath, "--api-socket", inst.SockPath,
			"--log-file", logPath,
			"--no-shutdown",
		)
	} else {
		cmd = exec.Command(d.binPath,
			"--api-socket", inst.SockPath,
			"--log-file", logPath,
			"--no-shutdown",
		)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start cloud-hypervisor binary: %w", err)
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	_ = os.WriteFile(filepath.Join(inst.Dir, "pid"), []byte(fmt.Sprintf("%d", pgid)), 0644)
	inst.Cmd = cmd
	inst.PGID = pgid

	// Wait for API socket to become active (up to 3 seconds)
	ready := false
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(inst.SockPath); err == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !ready {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return fmt.Errorf("timed out waiting for cloud-hypervisor socket %s", inst.SockPath)
	}

	// Send vm.create payload
	client := d.socketClient(inst.SockPath)
	createReq := map[string]any{
		"cpus": map[string]any{
			"boot_vcpus": inst.VM.VCPUs,
			"max_vcpus":  inst.VM.VCPUs,
		},
		"memory": map[string]any{
			"size": memoryBytes,
		},
		"disks": []map[string]any{
			{
				"path":       inst.DiskPath,
				"image_type": "Raw",
			},
		},
		"serial": map[string]any{
			"mode":   "Socket",
			"socket": inst.VM.SerialSocket,
		},
	}
	if d.initramfsPath != "" {
		createReq["payload"] = map[string]any{
			"kernel":    d.kernelPath,
			"initramfs": d.initramfsPath,
			"cmdline":   "console=ttyS0 quiet panic=-1",
		}
	} else if d.kernelPath != "" {
		createReq["payload"] = map[string]any{
			"kernel":  d.kernelPath,
			"cmdline": "console=ttyS0 panic=-1",
		}
	}

	reqBody, _ := json.Marshal(createReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/api/v1/vm.create", bytes.NewReader(reqBody))
	if err != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return fmt.Errorf("failed to build create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return fmt.Errorf("vm.create call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return fmt.Errorf("hypervisor rejected vm.create: %s (status %d)", string(body), resp.StatusCode)
	}

	return nil
}

// RecoverVM re-attaches a previously configured VM instance on disk to Cloud Hypervisor driver.
func (d *CloudHypervisorDriver) RecoverVM(ctx context.Context, vm *models.VM) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	vmDir := filepath.Join(d.baseDir, vm.ID)
	sockPath := filepath.Join(vmDir, "ch.sock")
	diskPath := filepath.Join(vmDir, "rootfs.raw")

	var pgid int
	if pidBytes, err := os.ReadFile(filepath.Join(vmDir, "pid")); err == nil {
		_, _ = fmt.Sscanf(string(pidBytes), "%d", &pgid)
	}

	inst := &CHVMInstance{
		VM:       *vm,
		State:    vm.State,
		Dir:      vmDir,
		SockPath: sockPath,
		DiskPath: diskPath,
		PGID:     pgid,
	}
	d.instances[vm.ID] = inst
	return nil
}

// BootVM starts the VM using the Cloud Hypervisor Unix socket API.
func (d *CloudHypervisorDriver) BootVM(ctx context.Context, vmID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, exists := d.instances[vmID]
	if !exists {
		return fmt.Errorf("vm %s not found", vmID)
	}

	client := d.socketClient(inst.SockPath)

	// Verify if process and socket are active; if not, re-spawn process
	processAlive := false
	if _, statErr := os.Stat(inst.SockPath); statErr == nil {
		testReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/vm.info", nil)
		if resp, err := client.Do(testReq); err == nil {
			_ = resp.Body.Close()
			processAlive = true
		}
	}
	if !processAlive {
		if err := d.spawnProcess(ctx, inst); err != nil {
			inst.State = models.StateError
			return fmt.Errorf("failed to spawn hypervisor process: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/api/v1/vm.boot", nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		inst.State = models.StateError
		return fmt.Errorf("vm.boot call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		inst.State = models.StateError
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hypervisor boot error: %s (status %d)", string(body), resp.StatusCode)
	}

	inst.State = models.StateRunning
	return nil
}

// StopVM shuts down the VM gracefully or shuts down the VMM.
func (d *CloudHypervisorDriver) StopVM(ctx context.Context, vmID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, exists := d.instances[vmID]
	if !exists {
		return fmt.Errorf("vm %s not found", vmID)
	}

	if inst.State == models.StateStopped {
		return nil
	}

	client := d.socketClient(inst.SockPath)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/api/v1/vm.shutdown", nil)
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
	}

	inst.State = models.StateStopped
	return nil
}

// DeleteVM terminates the process group, cleans up socket, and removes the disk.
func (d *CloudHypervisorDriver) DeleteVM(ctx context.Context, vmID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, exists := d.instances[vmID]
	if exists {
		// Shutdown VMM socket API or send SIGKILL to process group
		client := d.socketClient(inst.SockPath)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/api/v1/vmm.shutdown", nil)
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}

		if inst.PGID > 0 {
			_ = syscall.Kill(-inst.PGID, syscall.SIGKILL)
		} else if inst.Cmd != nil && inst.Cmd.Process != nil {
			_ = inst.Cmd.Process.Kill()
		}

		_ = os.RemoveAll(inst.Dir)
		delete(d.instances, vmID)
	} else {
		vmDir := filepath.Join(d.baseDir, vmID)
		_ = os.RemoveAll(vmDir)
	}

	return nil
}

// GetVMStatus inspects process and hypervisor status.
func (d *CloudHypervisorDriver) GetVMStatus(ctx context.Context, vmID string) (models.VMState, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	inst, exists := d.instances[vmID]
	if !exists {
		return "", fmt.Errorf("vm %s not found", vmID)
	}

	// Verify if process is still running
	if inst.Cmd != nil && inst.Cmd.Process != nil {
		// Signal 0 checks process existence without killing
		if err := inst.Cmd.Process.Signal(syscall.Signal(0)); err != nil {
			if inst.State == models.StateStopped {
				return models.StateStopped, nil
			}
			return models.StateError, fmt.Errorf("vm process died unexpectedly: %w", err)
		}
	} else if inst.PGID > 0 {
		if err := syscall.Kill(inst.PGID, syscall.Signal(0)); err != nil {
			if inst.State == models.StateStopped {
				return models.StateStopped, nil
			}
			return models.StateError, fmt.Errorf("vm process died unexpectedly: %w", err)
		}
	}

	client := d.socketClient(inst.SockPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/vm.info", nil)
	if err != nil {
		return inst.State, nil
	}

	resp, err := client.Do(req)
	if err != nil {
		// Socket unreachable
		if inst.State == models.StateStopped {
			return models.StateStopped, nil
		}
		return models.StateError, fmt.Errorf("hypervisor socket unreachable: %w", err)
	}
	defer resp.Body.Close()

	var info struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err == nil {
		switch info.State {
		case "Running":
			return models.StateRunning, nil
		case "Created":
			return models.StateCreated, nil
		case "Paused", "Inactive":
			return models.StateStopped, nil
		}
	}

	return inst.State, nil
}

// CleanupOrphans cleans up untracked directories, stale sockets, and processes.
func (d *CloudHypervisorDriver) CleanupOrphans(ctx context.Context, activeVMIDs []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	activeMap := make(map[string]bool)
	for _, id := range activeVMIDs {
		activeMap[id] = true
	}

	entries, err := os.ReadDir(d.baseDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			vmID := entry.Name()
			if !activeMap[vmID] {
				orphanDir := filepath.Join(d.baseDir, vmID)
				_ = os.RemoveAll(orphanDir)
				delete(d.instances, vmID)
			}
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// GetVMResourceUsage inspects /proc for the Cloud Hypervisor process and reports live CPU and RAM usage.
func (d *CloudHypervisorDriver) GetVMResourceUsage(ctx context.Context, vmID string) (*models.VMResourceUsage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, exists := d.instances[vmID]
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

	pid := inst.PGID
	if inst.Cmd != nil && inst.Cmd.Process != nil && inst.Cmd.Process.Pid > 0 {
		pid = inst.Cmd.Process.Pid
	}
	if pid <= 0 {
		return &models.VMResourceUsage{
			CPUPercent:    0,
			MemoryUsedMB:  0,
			MemoryLimitMB: inst.VM.MemoryMB,
			MemoryPercent: 0,
		}, nil
	}

	// 1. Read Resident Set Size (RSS) memory from /proc/<pid>/status
	statusFile := fmt.Sprintf("/proc/%d/status", pid)
	memUsedMB := 0
	if statusData, err := os.ReadFile(statusFile); err == nil {
		lines := strings.Split(string(statusData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					var kb int
					if _, err := fmt.Sscanf(fields[1], "%d", &kb); err == nil && kb > 0 {
						memUsedMB = int(math.Round(float64(kb) / 1024.0))
						if memUsedMB == 0 {
							memUsedMB = 1
						}
					}
				}
				break
			}
		}
	}

	memPct := 0.0
	if inst.VM.MemoryMB > 0 {
		memPct = math.Round((float64(memUsedMB)/float64(inst.VM.MemoryMB))*1000) / 10
		if memPct > 100.0 {
			memPct = 100.0
		}
	}

	// 2. Read CPU ticks (utime + stime) from /proc/<pid>/stat
	statFile := fmt.Sprintf("/proc/%d/stat", pid)
	now := time.Now()
	if statData, err := os.ReadFile(statFile); err == nil {
		fields := strings.Fields(string(statData))
		if len(fields) >= 15 {
			var utime, stime uint64
			_, _ = fmt.Sscanf(fields[13], "%d", &utime)
			_, _ = fmt.Sscanf(fields[14], "%d", &stime)
			currentTicks := utime + stime

			if inst.lastCPUTime.IsZero() {
				inst.lastCPUTime = now
				inst.lastCPUTicks = currentTicks
				inst.lastCPUUsage = 0.5
			} else {
				deltaSec := now.Sub(inst.lastCPUTime).Seconds()
				if deltaSec >= 0.5 {
					if currentTicks >= inst.lastCPUTicks {
						deltaTicks := currentTicks - inst.lastCPUTicks
						inst.lastCPUUsage = (float64(deltaTicks) / 100.0 / deltaSec) * 100.0
					}
					inst.lastCPUTicks = currentTicks
					inst.lastCPUTime = now
				}
			}
		}
	}

	cpuPercent := math.Round(inst.lastCPUUsage*10) / 10
	if cpuPercent < 0 {
		cpuPercent = 0
	}

	return &models.VMResourceUsage{
		CPUPercent:    cpuPercent,
		MemoryUsedMB:  memUsedMB,
		MemoryLimitMB: inst.VM.MemoryMB,
		MemoryPercent: memPct,
	}, nil
}
