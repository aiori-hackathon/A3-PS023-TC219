package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"vm-controller/src/pkg/models"
)

func TestSQLiteStoreLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	vm1 := &models.VM{
		ID:           "vm-test-1",
		Name:         "database-app",
		VCPUs:        2,
		MemoryMB:     512,
		State:        models.StateCreated,
		HostID:       "worker-1",
		IPAddress:    "127.0.0.1",
		OS:           "MINIX 3",
		SSHPort:      2201,
		SerialSocket: "/tmp/serial.sock",
		ErrorMessage: "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// 1. Insert VM
	if err := s.SaveVM(ctx, vm1); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	// 2. Retrieve VM
	got, err := s.GetVM(ctx, "vm-test-1")
	if err != nil {
		t.Fatalf("failed to get VM: %v", err)
	}
	if got.ID != vm1.ID || got.Name != vm1.Name || got.State != vm1.State || got.SSHPort != vm1.SSHPort {
		t.Errorf("retrieved VM mismatch: got %+v, want %+v", got, vm1)
	}

	// 3. Update VM (Atomic Upsert)
	vm1.State = models.StateRunning
	vm1.SSHPort = 2205
	vm1.UpdatedAt = time.Now()
	if err := s.SaveVM(ctx, vm1); err != nil {
		t.Fatalf("failed to update VM: %v", err)
	}

	updated, err := s.GetVM(ctx, "vm-test-1")
	if err != nil {
		t.Fatalf("failed to get updated VM: %v", err)
	}
	if updated.State != models.StateRunning || updated.SSHPort != 2205 {
		t.Errorf("expected updated state RUNNING and port 2205, got %s and %d", updated.State, updated.SSHPort)
	}

	// 4. Insert Second VM and List
	vm2 := &models.VM{
		ID:        "vm-test-2",
		Name:      "web-app",
		VCPUs:     1,
		MemoryMB:  256,
		State:     models.StateStopped,
		HostID:    "worker-2",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	if err := s.SaveVM(ctx, vm2); err != nil {
		t.Fatalf("failed to save vm2: %v", err)
	}

	list, err := s.ListVMs(ctx)
	if err != nil {
		t.Fatalf("failed to list VMs: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 VMs in list, got %d", len(list))
	}

	// 5. Delete VM
	if err := s.DeleteVM(ctx, "vm-test-1"); err != nil {
		t.Fatalf("failed to delete VM: %v", err)
	}

	_, err = s.GetVM(ctx, "vm-test-1")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted VM, got %v", err)
	}

	listAfterDelete, err := s.ListVMs(ctx)
	if err != nil {
		t.Fatalf("failed to list after delete: %v", err)
	}
	if len(listAfterDelete) != 1 || listAfterDelete[0].ID != "vm-test-2" {
		t.Errorf("expected 1 VM remaining with ID vm-test-2, got %+v", listAfterDelete)
	}
}
