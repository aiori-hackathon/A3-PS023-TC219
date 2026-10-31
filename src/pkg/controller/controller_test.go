package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"vm-controller/src/pkg/models"
	"vm-controller/src/pkg/worker"
)

type testCluster struct {
	w1Server   *httptest.Server
	w1         *worker.Worker
	w1Dir      string
	w2Server   *httptest.Server
	w2         *worker.Worker
	w2Dir      string
	ctrl       *Controller
	ctrlServer *httptest.Server
	ctrlDir    string
}

func (c *testCluster) Close() {
	if c.ctrlServer != nil {
		c.ctrlServer.Close()
	}
	if c.ctrl != nil {
		c.ctrl.Close()
	}
	if c.w1Server != nil {
		c.w1Server.Close()
	}
	if c.w1 != nil {
		c.w1.Close()
	}
	if c.w2Server != nil {
		c.w2Server.Close()
	}
	if c.w2 != nil {
		c.w2.Close()
	}
	if c.w1Dir != "" {
		_ = os.RemoveAll(c.w1Dir)
	}
	if c.w2Dir != "" {
		_ = os.RemoveAll(c.w2Dir)
	}
	if c.ctrlDir != "" {
		_ = os.RemoveAll(c.ctrlDir)
	}
}

func setupTestCluster(t *testing.T, w1TotalMem, w2TotalMem int) *testCluster {
	d1, err := os.MkdirTemp("", "cluster-w1-*")
	if err != nil {
		t.Fatalf("failed to create w1 dir: %v", err)
	}
	d2, err := os.MkdirTemp("", "cluster-w2-*")
	if err != nil {
		t.Fatalf("failed to create w2 dir: %v", err)
	}
	dCtrl, err := os.MkdirTemp("", "cluster-ctrl-*")
	if err != nil {
		t.Fatalf("failed to create ctrl dir: %v", err)
	}

	w1 := worker.NewWorker(worker.Config{
		ID:               "worker-1",
		Token:            "w-token-1",
		TotalMemoryMB:    w1TotalMem,
		ReservedMemoryMB: 512,
		DataDir:          d1,
		Driver:           worker.NewMockHypervisor(d1),
	})
	w1Server := httptest.NewServer(worker.NewHandler(w1))

	w2 := worker.NewWorker(worker.Config{
		ID:               "worker-2",
		Token:            "w-token-2",
		TotalMemoryMB:    w2TotalMem,
		ReservedMemoryMB: 512,
		DataDir:          d2,
		Driver:           worker.NewMockHypervisor(d2),
	})
	w2Server := httptest.NewServer(worker.NewHandler(w2))

	ctrl := NewController(Config{
		Addr:    "127.0.0.1:8080",
		Token:   "ctrl-secret",
		DataDir: dCtrl,
		Workers: []WorkerTarget{
			{ID: "worker-1", URL: w1Server.URL, Token: "w-token-1"},
			{ID: "worker-2", URL: w2Server.URL, Token: "w-token-2"},
		},
	})
	ctrlServer := httptest.NewServer(NewHandler(ctrl))

	return &testCluster{
		w1Server:   w1Server,
		w1:         w1,
		w1Dir:      d1,
		w2Server:   w2Server,
		w2:         w2,
		w2Dir:      d2,
		ctrl:       ctrl,
		ctrlServer: ctrlServer,
		ctrlDir:    dCtrl,
	}
}

func TestControllerPlacementMemoryAware(t *testing.T) {
	// Worker 1: 1536MB total (free after 512MB reserve = 1024MB)
	// Worker 2: 3072MB total (free after 512MB reserve = 2560MB)
	cluster := setupTestCluster(t, 1536, 3072)
	defer cluster.Close()

	ctx := context.Background()

	// VM requires 512MB. Worker 2 has much more free memory -> should be placed on Worker 2
	vm, err := cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-place-1",
		Name:     "app-large",
		VCPUs:    2,
		MemoryMB: 512,
	})
	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	if vm.HostID != "worker-2" {
		t.Errorf("expected placement on worker-2 (most free memory), got %s", vm.HostID)
	}
}

func TestControllerExplicitWorkerSelection(t *testing.T) {
	// Worker 1: 3072MB (free: 2560MB)
	// Worker 2: 1536MB (free: 1024MB)
	cluster := setupTestCluster(t, 3072, 1536)
	defer cluster.Close()

	ctx := context.Background()

	// 1. Explicitly select worker-2 (even though worker-1 has more free memory)
	vm, err := cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-explicit-w2",
		Name:     "app-pin-w2",
		VCPUs:    1,
		MemoryMB: 256,
		WorkerID: "worker-2",
	})
	if err != nil {
		t.Fatalf("failed to create VM with explicit worker-2: %v", err)
	}
	if vm.HostID != "worker-2" {
		t.Errorf("expected placement on explicitly selected worker-2, got %s", vm.HostID)
	}

	// 2. Explicitly select non-existent worker -> expect error
	_, err = cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-invalid-worker",
		Name:     "app-bad-worker",
		VCPUs:    1,
		MemoryMB: 256,
		WorkerID: "worker-999",
	})
	if err == nil {
		t.Fatalf("expected error for non-existent worker, got nil")
	}

	// 3. Explicitly select worker with insufficient memory -> expect error
	_, err = cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-too-big-for-w2",
		Name:     "app-overflow-w2",
		VCPUs:    1,
		MemoryMB: 2000, // worker-2 only has 1024MB free
		WorkerID: "worker-2",
	})
	if err == nil {
		t.Fatalf("expected error for insufficient memory on worker-2, got nil")
	}
}

func TestControllerPlacementTieBreaking(t *testing.T) {
	// Both workers have identical total memory (2048 MB)
	cluster := setupTestCluster(t, 2048, 2048)
	defer cluster.Close()

	ctx := context.Background()

	// Create VM 1 on the cluster
	vm1, err := cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-1",
		Name:     "first-app",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err != nil {
		t.Fatalf("failed to create vm1: %v", err)
	}

	// Now start vm1 so runningVMs increments on that host
	_, err = cluster.ctrl.StartVM(ctx, vm1.ID)
	if err != nil {
		t.Fatalf("failed to start vm1: %v", err)
	}

	// Create VM 2. Because vm1's host now has less free memory and more running VMs,
	// vm2 MUST be placed on the other worker!
	vm2, err := cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-2",
		Name:     "second-app",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err != nil {
		t.Fatalf("failed to create vm2: %v", err)
	}

	if vm2.HostID == vm1.HostID {
		t.Errorf("expected vm2 to be placed on opposite host to balance load, but got %s for both", vm1.HostID)
	}
}

func TestControllerPlacementInsufficientMemory(t *testing.T) {
	// Hosts have only 600MB total memory.
	// After 512MB reserve, only 88MB is free!
	cluster := setupTestCluster(t, 600, 600)
	defer cluster.Close()

	// Try to create a VM requiring 256MB. Both hosts cannot satisfy this request.
	body, _ := json.Marshal(models.CreateVMRequest{
		ID:       "vm-heavy",
		Name:     "heavy-app",
		VCPUs:    2,
		MemoryMB: 256,
	})

	req, _ := http.NewRequest(http.MethodPost, cluster.ctrlServer.URL+"/vms", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer ctrl-secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := cluster.ctrlServer.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("expected HTTP 507 Insufficient Storage when no host fits request, got %d", resp.StatusCode)
	}
}

func TestControllerLifecycleAPI(t *testing.T) {
	cluster := setupTestCluster(t, 2048, 2048)
	defer cluster.Close()

	client := cluster.ctrlServer.Client()

	// 1. Auth failure test
	req, _ := http.NewRequest(http.MethodGet, cluster.ctrlServer.URL+"/vms", nil)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without Bearer token, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Create VM via API
	createBody, _ := json.Marshal(models.CreateVMRequest{
		ID:       "api-vm-1",
		Name:     "web-api-vm",
		VCPUs:    2,
		MemoryMB: 512,
	})
	req, _ = http.NewRequest(http.MethodPost, cluster.ctrlServer.URL+"/vms", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer ctrl-secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on create VM, got status: %d", resp.StatusCode)
	}
	var apiResp struct {
		Success bool      `json:"success"`
		Data    models.VM `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&apiResp)
	resp.Body.Close()

	if apiResp.Data.State != models.StateCreated {
		t.Fatalf("expected state CREATED, got: %s", apiResp.Data.State)
	}
	if apiResp.Data.OS != "MINIX 3" {
		t.Errorf("expected OS 'MINIX 3', got %s", apiResp.Data.OS)
	}
	if apiResp.Data.IPAddress == "" {
		t.Errorf("expected valid IP address, got empty string")
	}

	// 3. Start VM via API
	req, _ = http.NewRequest(http.MethodPost, cluster.ctrlServer.URL+"/vms/api-vm-1/start", nil)
	req.Header.Set("Authorization", "Bearer ctrl-secret")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on start VM, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 4. Starting already RUNNING VM returns 409 Conflict
	resp, err = client.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict on redundant start, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 5. Stop VM via API
	req, _ = http.NewRequest(http.MethodPost, cluster.ctrlServer.URL+"/vms/api-vm-1/stop", nil)
	req.Header.Set("Authorization", "Bearer ctrl-secret")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on stop VM, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 6. Idempotent stop: stopping already STOPPED VM returns 200 OK
	resp, err = client.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK on idempotent stop, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 7. Delete VM via API
	req, _ = http.NewRequest(http.MethodDelete, cluster.ctrlServer.URL+"/vms/api-vm-1", nil)
	req.Header.Set("Authorization", "Bearer ctrl-secret")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on delete VM, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 8. Starting deleted VM returns 404
	req, _ = http.NewRequest(http.MethodPost, cluster.ctrlServer.URL+"/vms/api-vm-1/start", nil)
	req.Header.Set("Authorization", "Bearer ctrl-secret")
	resp, err = client.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found after deletion, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestControllerWorkerRecovery(t *testing.T) {
	cluster := setupTestCluster(t, 2048, 2048)
	defer cluster.Close()

	ctx := context.Background()

	// Create and start a VM
	vm, err := cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-recov",
		Name:     "recov-vm",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err != nil {
		t.Fatalf("failed to create vm: %v", err)
	}

	_, err = cluster.ctrl.StartVM(ctx, vm.ID)
	if err != nil {
		t.Fatalf("failed to start vm: %v", err)
	}

	// Shut down the worker hosting this VM to simulate host outage
	if vm.HostID == "worker-1" {
		cluster.w1Server.Close()
		cluster.w1.Close()
	} else {
		cluster.w2Server.Close()
		cluster.w2.Close()
	}

	// Run controller reconciler 3 times to trigger consecutive failure threshold
	for i := 0; i < 3; i++ {
		cluster.ctrl.reconcile()
	}

	recoveredVM, err := cluster.ctrl.GetVM(vm.ID)
	if err != nil {
		t.Fatalf("failed to get vm: %v", err)
	}

	// Controller should have detected host failure and updated state to ERROR
	if recoveredVM.State != models.StateError {
		t.Errorf("expected VM state to recover to ERROR when worker is down, got: %s", recoveredVM.State)
	}
}

func TestUIEndpoint(t *testing.T) {
	cluster := setupTestCluster(t, 2048, 2048)
	defer cluster.Close()

	resp, err := http.Get(cluster.ctrlServer.URL + "/")
	if err != nil {
		t.Fatalf("failed to GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for UI endpoint, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Cloud Hypervisor VM Controller") {
		t.Errorf("expected UI HTML body, got: %s", string(body))
	}
}

func TestControllerRestartRecovery(t *testing.T) {
	cluster := setupTestCluster(t, 2048, 2048)
	defer cluster.Close()

	ctx := context.Background()

	// 1. Create and start VM on cluster
	vm, err := cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{
		ID:       "vm-ctrl-restart",
		Name:     "app-restart",
		VCPUs:    1,
		MemoryMB: 256,
	})
	if err != nil {
		t.Fatalf("failed to create vm: %v", err)
	}

	startedVM, err := cluster.ctrl.StartVM(ctx, vm.ID)
	if err != nil {
		t.Fatalf("failed to start vm: %v", err)
	}
	if startedVM.State != models.StateRunning {
		t.Fatalf("expected state RUNNING, got %s", startedVM.State)
	}

	origSSHPort := startedVM.SSHPort
	origHostID := startedVM.HostID

	// 2. Shut down Controller (simulating controller process restart)
	cluster.ctrlServer.Close()
	cluster.ctrl.Close()

	// 3. Launch a new Controller pointing to the SAME DataDir and workers
	restartedCtrl := NewController(Config{
		Addr:    "127.0.0.1:8080",
		Token:   "ctrl-secret",
		DataDir: cluster.ctrlDir,
		Workers: []WorkerTarget{
			{ID: "worker-1", URL: cluster.w1Server.URL, Token: "w-token-1"},
			{ID: "worker-2", URL: cluster.w2Server.URL, Token: "w-token-2"},
		},
	})

	// Verify VM was recovered from disk upon restart
	recoveredVM, err := restartedCtrl.GetVM("vm-ctrl-restart")
	if err != nil {
		t.Fatalf("expected vm-ctrl-restart to be recovered by new controller: %v", err)
	}
	if recoveredVM.State != models.StateRunning {
		t.Errorf("expected recovered state RUNNING, got %s", recoveredVM.State)
	}
	if recoveredVM.SSHPort != origSSHPort {
		t.Errorf("expected SSH port %d, got %d", origSSHPort, recoveredVM.SSHPort)
	}
	if recoveredVM.HostID != origHostID {
		t.Errorf("expected HostID %s, got %s", origHostID, recoveredVM.HostID)
	}

	// 4. Verify controller can continue managing the recovered VM
	stoppedVM, err := restartedCtrl.StopVM(ctx, "vm-ctrl-restart")
	if err != nil {
		t.Fatalf("failed to stop recovered VM through restarted controller: %v", err)
	}
	if stoppedVM.State != models.StateStopped {
		t.Errorf("expected state STOPPED, got %s", stoppedVM.State)
	}

	// Shut down restarted controller before testing fresh controller auto-discovery
	restartedCtrl.Close()

	// 5. Verify Worker Auto-Discovery:
	// A completely fresh controller with NO previous DataDir discovers existing VMs from workers
	freshDir, err := os.MkdirTemp("", "fresh-ctrl-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(freshDir)

	freshCtrl := NewController(Config{
		Addr:    "127.0.0.1:8080",
		Token:   "ctrl-secret",
		DataDir: freshDir,
		Workers: []WorkerTarget{
			{ID: "worker-1", URL: cluster.w1Server.URL, Token: "w-token-1"},
			{ID: "worker-2", URL: cluster.w2Server.URL, Token: "w-token-2"},
		},
	})
	defer freshCtrl.Close()

	// Trigger reconciliation to sync with live workers
	freshCtrl.Reconcile()

	discoveredList := freshCtrl.ListVMs()
	if len(discoveredList) != 1 {
		t.Fatalf("expected fresh controller to discover 1 VM from workers, got %d", len(discoveredList))
	}
	if discoveredList[0].ID != "vm-ctrl-restart" {
		t.Errorf("expected discovered VM ID vm-ctrl-restart, got %s", discoveredList[0].ID)
	}
	if discoveredList[0].State != models.StateStopped {
		t.Errorf("expected discovered state STOPPED, got %s", discoveredList[0].State)
	}
}

func TestWorkerAndVMOrderStability(t *testing.T) {
	cluster := setupTestCluster(t, 2048, 2048)
	defer cluster.Close()

	ctx := context.Background()

	// 1. Verify worker list ordering is 100% deterministic (worker-1 always precedes worker-2)
	for i := 0; i < 10; i++ {
		workers := cluster.ctrl.GetWorkerInfos(ctx)
		if len(workers) != 2 {
			t.Fatalf("expected 2 workers, got %d", len(workers))
		}
		if workers[0].ID != "worker-1" || workers[1].ID != "worker-2" {
			t.Errorf("iteration %d: expected [worker-1, worker-2], got [%s, %s]", i, workers[0].ID, workers[1].ID)
		}
	}

	// 2. Deploy multiple VMs
	_, _ = cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{ID: "vm-alpha", Name: "app-a", VCPUs: 1, MemoryMB: 256})
	_, _ = cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{ID: "vm-beta", Name: "app-b", VCPUs: 1, MemoryMB: 256})
	_, _ = cluster.ctrl.CreateVM(ctx, models.CreateVMRequest{ID: "vm-gamma", Name: "app-c", VCPUs: 1, MemoryMB: 256})

	// 3. Verify ListVMs maintains identical deterministic order across repeated calls
	var expectedOrder []string
	for _, v := range cluster.ctrl.ListVMs() {
		expectedOrder = append(expectedOrder, v.ID)
	}

	for i := 0; i < 10; i++ {
		list := cluster.ctrl.ListVMs()
		if len(list) != len(expectedOrder) {
			t.Fatalf("iteration %d: expected %d VMs, got %d", i, len(expectedOrder), len(list))
		}
		for j := range list {
			if list[j].ID != expectedOrder[j] {
				t.Errorf("iteration %d: order changed at index %d: expected %s, got %s", i, j, expectedOrder[j], list[j].ID)
			}
		}
	}
}
