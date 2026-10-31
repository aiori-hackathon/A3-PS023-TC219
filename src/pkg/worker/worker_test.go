package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"vm-controller/src/pkg/models"

	"golang.org/x/crypto/ssh"
)

func setupTestWorker(t *testing.T) (*Worker, *Handler, string) {
	tempDir, err := os.MkdirTemp("", "worker-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	driver := NewMockHypervisor(tempDir)
	w := NewWorker(Config{
		ID:               "test-worker-1",
		Token:            "test-token",
		TotalMemoryMB:    2048,
		ReservedMemoryMB: 512,
		DataDir:          tempDir,
		Driver:           driver,
	})

	handler := NewHandler(w)
	return w, handler, tempDir
}

func TestWorkerLifecycle(t *testing.T) {
	w, handler, tempDir := setupTestWorker(t)
	defer os.RemoveAll(tempDir)
	defer w.Close()

	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()

	// 1. Health check (unauthenticated)
	resp, err := client.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Resources check (authenticated)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/resources", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("resources call failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var res models.HostResources
	_ = json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()

	if res.TotalMemoryMB != 2048 || res.ReservedMemoryMB != 512 || res.FreeMemoryMB != 1536 {
		t.Errorf("unexpected resources: %+v", res)
	}

	// 3. Unauthorized access check
	badReq, _ := http.NewRequest(http.MethodGet, server.URL+"/resources", nil)
	badReq.Header.Set("Authorization", "Bearer wrong-token")
	badResp, err := client.Do(badReq)
	if err != nil {
		t.Fatalf("unauth call failed: %v", err)
	}
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", badResp.StatusCode)
	}
	badResp.Body.Close()

	// 4. Create VM
	createPayload := models.CreateVMRequest{
		ID:       "vm-1",
		Name:     "web-1",
		VCPUs:    2,
		MemoryMB: 512,
	}
	body, _ := json.Marshal(createPayload)
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/vms", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("create VM call failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on create, got %d", resp.StatusCode)
	}
	var createResp models.APIResponse
	_ = json.NewDecoder(resp.Body).Decode(&createResp)
	resp.Body.Close()

	vm, err := w.GetVM("vm-1")
	if err != nil || vm.State != models.StateCreated {
		t.Fatalf("expected VM in CREATED state, got state: %s, err: %v", vm.State, err)
	}

	// Verify disk and socket were created
	if _, err := os.Stat(filepath.Join(tempDir, "vm-1", "ch.sock")); err != nil {
		t.Errorf("expected socket file to exist: %v", err)
	}

	// 5. Start VM
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/vms/vm-1/start", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on start, got status: %d, err: %v", resp.StatusCode, err)
	}
	resp.Body.Close()

	vm, _ = w.GetVM("vm-1")
	if vm.State != models.StateRunning {
		t.Fatalf("expected VM in RUNNING state, got: %s", vm.State)
	}
	if vm.ResourceUsage == nil {
		t.Errorf("expected ResourceUsage to be populated for RUNNING VM")
	} else if vm.ResourceUsage.MemoryUsedMB <= 0 {
		t.Errorf("expected MemoryUsedMB > 0 for running VM, got %d", vm.ResourceUsage.MemoryUsedMB)
	}

	// 6. Starting already running VM should return 409 Conflict
	resp, err = client.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict when starting running VM, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 7. Stop VM
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/vms/vm-1/stop", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on stop, got: %d", resp.StatusCode)
	}
	resp.Body.Close()

	vm, _ = w.GetVM("vm-1")
	if vm.State != models.StateStopped {
		t.Fatalf("expected VM in STOPPED state, got: %s", vm.State)
	}

	// 8. Idempotent stop: stopping already STOPPED VM should return 200 OK!
	resp, err = client.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK on idempotent stop, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 9. Delete VM
	req, _ = http.NewRequest(http.MethodDelete, server.URL+"/vms/vm-1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on delete, got: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Directory and files should be cleaned up
	if _, err := os.Stat(filepath.Join(tempDir, "vm-1")); !os.IsNotExist(err) {
		t.Errorf("expected VM directory to be removed, but it still exists")
	}
}

func TestWorkerFailureRollbackAndRecovery(t *testing.T) {
	w, _, tempDir := setupTestWorker(t)
	defer os.RemoveAll(tempDir)
	defer w.Close()

	// Failure rollback during creation
	_, err := w.CreateVM(context.Background(), models.CreateVMRequest{
		ID:       "vm-fail",
		Name:     "inject-fail-create-vm",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err == nil {
		t.Fatalf("expected create error from injected failure, got nil")
	}

	// Verify VM was rolled back and not leaked in tracking or disk
	if _, getErr := w.GetVM("vm-fail"); getErr == nil {
		t.Errorf("expected vm-fail to not exist in tracking after rollback")
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "vm-fail")); !os.IsNotExist(statErr) {
		t.Errorf("expected vm-fail directory to be cleaned up after rollback")
	}

	// Test Crash Recovery: VM running, then crashes
	normalVM, err := w.CreateVM(context.Background(), models.CreateVMRequest{
		ID:       "vm-crash",
		Name:     "vm-to-crash",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err != nil {
		t.Fatalf("failed to create normal vm: %v", err)
	}
	if err := w.BootVM(context.Background(), normalVM.ID); err != nil {
		t.Fatalf("failed to boot normal vm: %v", err)
	}

	// Trigger simulated crash on hypervisor driver
	mockDriver := w.driver.(*MockHypervisor)
	mockDriver.SimulateCrash(normalVM.ID)

	// Trigger worker reconcile manually (3 times to cross threshold)
	for i := 0; i < 3; i++ {
		w.reconcile()
	}

	crashedVM, err := w.GetVM("vm-crash")
	if err != nil {
		t.Fatalf("failed to get crashed vm: %v", err)
	}
	if crashedVM.State != models.StateError {
		t.Errorf("expected crashed VM state to be recovered to ERROR, got: %s", crashedVM.State)
	}

	// Clean up
	_ = w.DeleteVM(context.Background(), "vm-crash")
}

func TestWorkerOrphanCleanup(t *testing.T) {
	w, _, tempDir := setupTestWorker(t)
	defer os.RemoveAll(tempDir)
	defer w.Close()

	// Create an orphaned directory mimicking an untracked/crashed VM
	orphanDir := filepath.Join(tempDir, "orphan-vm-999")
	_ = os.MkdirAll(orphanDir, 0755)
	_ = os.WriteFile(filepath.Join(orphanDir, "ch.sock"), []byte("orphan"), 0600)

	// Run reconcile / orphan cleanup
	w.reconcile()

	time.Sleep(50 * time.Millisecond)

	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Errorf("expected orphan directory to be removed by reconciler, but it exists")
	}
}

func TestRealCloudHypervisorLifecycle(t *testing.T) {
	if _, err := exec.LookPath("cloud-hypervisor"); err != nil {
		t.Skip("cloud-hypervisor binary not found, skipping real hypervisor test")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm not found, skipping real hypervisor test")
	}

	tempDir, err := os.MkdirTemp("", "ch-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	driver := NewCloudHypervisorDriver(tempDir, "cloud-hypervisor", "", "")
	w := NewWorker(Config{
		ID:               "real-worker",
		Token:            "test-token",
		TotalMemoryMB:    4096,
		ReservedMemoryMB: 512,
		Driver:           driver,
	})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Create real VM with Cloud Hypervisor
	vm, err := w.CreateVM(ctx, models.CreateVMRequest{
		ID:       "real-vm-1",
		Name:     "real-minix-vm",
		VCPUs:    1,
		MemoryMB: 512,
	})
	if err != nil {
		t.Fatalf("failed to create real VM: %v", err)
	}
	if vm.State != models.StateCreated {
		t.Errorf("expected state CREATED, got %s", vm.State)
	}

	// 2. Boot real VM with Cloud Hypervisor
	if err := w.BootVM(ctx, vm.ID); err != nil {
		t.Fatalf("failed to boot real VM: %v", err)
	}

	vm, _ = w.GetVM(vm.ID)
	if vm.State != models.StateRunning {
		t.Errorf("expected state RUNNING, got %s", vm.State)
	}

	// Verify status from real hypervisor
	status, err := driver.GetVMStatus(ctx, vm.ID)
	if err != nil || status != models.StateRunning {
		t.Errorf("expected driver status RUNNING, got %s (err: %v)", status, err)
	}

	// 2.5 Verify live SSH connection to the guest shell via the SSH bridge
	if vm.SSHPort <= 0 {
		t.Errorf("expected SSHPort to be allocated (> 0), got %d", vm.SSHPort)
	} else {
		sshConfig := &ssh.ClientConfig{
			User:            "minix",
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         3 * time.Second,
		}
		sshClient, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", vm.SSHPort), sshConfig)
		if err != nil {
			t.Fatalf("failed to dial SSH bridge on port %d: %v", vm.SSHPort, err)
		}
		defer sshClient.Close()

		session, err := sshClient.NewSession()
		if err != nil {
			t.Fatalf("failed to create SSH session: %v", err)
		}
		defer session.Close()

		stdinPipe, _ := session.StdinPipe()
		stdoutPipe, _ := session.StdoutPipe()

		modes := ssh.TerminalModes{
			ssh.ECHO:          0,
			ssh.TTY_OP_ISPEED: 115200,
			ssh.TTY_OP_OSPEED: 115200,
		}
		if err := session.RequestPty("vt100", 80, 40, modes); err != nil {
			t.Fatalf("request pty failed: %v", err)
		}

		if err := session.Shell(); err != nil {
			t.Fatalf("failed to start remote shell: %v", err)
		}

		buf := make([]byte, 1024)
		readDone := make(chan bool)
		var output string
		sentCommand := false

		go func() {
			for {
				n, err := stdoutPipe.Read(buf)
				if n > 0 {
					chunk := string(buf[:n])
					output += chunk
					if !sentCommand && (strings.Contains(output, "~ #") || strings.Contains(output, "Interactive shell ready")) {
						sentCommand = true
						go func() {
							time.Sleep(200 * time.Millisecond)
							_, _ = stdinPipe.Write([]byte("echo SSH_TEST_HELLO_WORLD\n"))
						}()
					}
					if strings.Contains(output, "SSH_TEST_HELLO_WORLD") {
						readDone <- true
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()

		select {
		case <-readDone:
			t.Logf("SSH Bridge Verified! Interactive shell responded with expected marker.")
		case <-time.After(5 * time.Second):
			t.Errorf("timed out waiting for SSH command response. Output received: %s", output)
		}
	}

	// 3. Stop real VM
	if err := w.StopVM(ctx, vm.ID); err != nil {
		t.Fatalf("failed to stop real VM: %v", err)
	}

	// 4. Delete real VM
	if err := w.DeleteVM(ctx, vm.ID); err != nil {
		t.Fatalf("failed to delete real VM: %v", err)
	}
}

func TestWorkerRestartRecovery(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worker-restart-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ctx := context.Background()

	// 1. Initial Worker instance
	driver1 := NewMockHypervisor(tempDir)
	w1 := NewWorker(Config{
		ID:               "worker-recov",
		Token:            "secret",
		TotalMemoryMB:    2048,
		ReservedMemoryMB: 512,
		DataDir:          tempDir,
		Driver:           driver1,
	})

	vm, err := w1.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-worker-restart",
		Name:     "persistent-app",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err != nil {
		t.Fatalf("failed to create vm: %v", err)
	}

	if err := w1.BootVM(ctx, vm.ID); err != nil {
		t.Fatalf("failed to boot vm: %v", err)
	}

	runningVM, err := w1.GetVM(vm.ID)
	if err != nil || runningVM.State != models.StateRunning {
		t.Fatalf("expected vm to be running, got: %+v (err: %v)", runningVM, err)
	}
	initialSSHPort := runningVM.SSHPort

	// 2. Shut down Worker 1 (simulating process termination / crash)
	w1.Close()

	// 3. Launch a new Worker instance on the same DataDir
	driver2 := NewMockHypervisor(tempDir)
	w2 := NewWorker(Config{
		ID:               "worker-recov",
		Token:            "secret",
		TotalMemoryMB:    2048,
		ReservedMemoryMB: 512,
		DataDir:          tempDir,
		Driver:           driver2,
	})
	defer w2.Close()

	// 4. Verify VM survived and was recovered into memory
	recoveredVM, err := w2.GetVM("vm-worker-restart")
	if err != nil {
		t.Fatalf("failed to find recovered vm on new worker: %v", err)
	}

	if recoveredVM.State != models.StateRunning {
		t.Errorf("expected recovered vm to be RUNNING, got %s", recoveredVM.State)
	}
	if recoveredVM.SSHPort != initialSSHPort {
		t.Errorf("expected SSH port %d, got %d", initialSSHPort, recoveredVM.SSHPort)
	}

	res := w2.GetResources()
	if res.RunningVMs != 1 {
		t.Errorf("expected 1 running VM in host resources, got %d", res.RunningVMs)
	}

	// 5. Verify the recovered VM can still be operated on
	if err := w2.StopVM(ctx, "vm-worker-restart"); err != nil {
		t.Fatalf("failed to stop recovered vm: %v", err)
	}
	stoppedVM, _ := w2.GetVM("vm-worker-restart")
	if stoppedVM.State != models.StateStopped {
		t.Errorf("expected state STOPPED, got %s", stoppedVM.State)
	}
}

func TestMockHypervisorSSHBridge(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mock-ssh-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	driver := NewMockHypervisor(tempDir)
	w := NewWorker(Config{
		ID:               "mock-worker",
		Token:            "test-token",
		TotalMemoryMB:    4096,
		ReservedMemoryMB: 512,
		DataDir:          tempDir,
		Driver:           driver,
	})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vm, err := w.CreateVM(ctx, models.CreateVMRequest{
		ID:       "mock-vm-1",
		Name:     "minix-shell-test",
		VCPUs:    1,
		MemoryMB: 512,
	})
	if err != nil {
		t.Fatalf("failed to create vm: %v", err)
	}

	if err := w.BootVM(ctx, vm.ID); err != nil {
		t.Fatalf("failed to start vm: %v", err)
	}

	runningVM, err := w.GetVM(vm.ID)
	if err != nil || runningVM.State != models.StateRunning {
		t.Fatalf("expected VM to be running, got state: %s, err: %v", runningVM.State, err)
	}
	if runningVM.SSHPort <= 0 {
		t.Fatalf("expected valid SSH port, got %d", runningVM.SSHPort)
	}

	// Connect to SSH bridge
	sshConfig := &ssh.ClientConfig{
		User:            "minix",
		Auth:            []ssh.AuthMethod{ssh.Password("minix")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}

	sshClient, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", runningVM.SSHPort), sshConfig)
	if err != nil {
		t.Fatalf("failed to dial SSH bridge: %v", err)
	}
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		t.Fatalf("failed to create SSH session: %v", err)
	}
	defer session.Close()

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("failed to get stdin pipe: %v", err)
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to get stdout pipe: %v", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("vt100", 80, 40, modes); err != nil {
		t.Fatalf("request pty failed: %v", err)
	}

	if err := session.Shell(); err != nil {
		t.Fatalf("failed to start remote shell: %v", err)
	}

	buf := make([]byte, 1024)
	readDone := make(chan bool)
	var output string
	sentCommand := false

	go func() {
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				output += chunk
				if !sentCommand && strings.Contains(output, "minix# ") {
					sentCommand = true
					go func() {
						time.Sleep(100 * time.Millisecond)
						_, _ = stdinPipe.Write([]byte("uname -a\r"))
					}()
				}
				if strings.Contains(output, "Minix localhost") {
					readDone <- true
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-readDone:
		t.Logf("Mock SSH Bridge Verified! Interactive shell returned: %s", output)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for shell response. Output so far: %s", output)
	}

	if err := w.StopVM(ctx, vm.ID); err != nil {
		t.Fatalf("failed to stop vm: %v", err)
	}
}
