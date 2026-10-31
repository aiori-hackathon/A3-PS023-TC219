package models

import (
	"testing"
)

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		from  VMState
		to    VMState
		valid bool
	}{
		{StateCreating, StateCreated, true},
		{StateCreating, StateError, true},
		{StateCreating, StateRunning, false},
		{StateCreated, StateRunning, true},
		{StateCreated, StateDeleted, true},
		{StateCreated, StateStopped, false},
		{StateRunning, StateStopped, true},
		{StateRunning, StateError, true},
		{StateRunning, StateDeleted, false},
		{StateStopped, StateRunning, true},
		{StateStopped, StateDeleted, true},
		{StateStopped, StateError, true},
		{StateError, StateDeleted, true},
		{StateDeleted, StateRunning, false},
		{StateDeleted, StateCreated, false},
	}

	for _, tt := range tests {
		got := IsValidTransition(tt.from, tt.to)
		if got != tt.valid {
			t.Errorf("IsValidTransition(%s -> %s) = %v; want %v", tt.from, tt.to, got, tt.valid)
		}
	}
}

func TestValidation(t *testing.T) {
	validReq := CreateVMRequest{
		Name:     "web-server",
		VCPUs:    2,
		MemoryMB: 1024,
	}
	if err := validReq.Validate(); err != nil {
		t.Fatalf("expected valid request, got err: %v", err)
	}

	// Invalid vCPU (less than 1)
	reqLowVCPU := validReq
	reqLowVCPU.VCPUs = 0
	if err := reqLowVCPU.Validate(); err == nil {
		t.Errorf("expected error for vCPUs = 0, got nil")
	}

	// Invalid vCPU (more than 4)
	reqHighVCPU := validReq
	reqHighVCPU.VCPUs = 5
	if err := reqHighVCPU.Validate(); err == nil {
		t.Errorf("expected error for vCPUs = 5, got nil")
	}

	// Invalid memory (< 128 MB)
	reqLowMem := validReq
	reqLowMem.MemoryMB = 64
	if err := reqLowMem.Validate(); err == nil {
		t.Errorf("expected error for memory = 64MB, got nil")
	}

	// Invalid memory (> 2048 MB)
	reqHighMem := validReq
	reqHighMem.MemoryMB = 4096
	if err := reqHighMem.Validate(); err == nil {
		t.Errorf("expected error for memory = 4096MB, got nil")
	}

	// Empty name
	reqNoName := validReq
	reqNoName.Name = ""
	if err := reqNoName.Validate(); err == nil {
		t.Errorf("expected error for empty name, got nil")
	}
}
