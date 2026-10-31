package store

import (
	"context"
	"vm-controller/src/pkg/models"
)

// Store defines persistent storage operations for VM records.
type Store interface {
	SaveVM(ctx context.Context, vm *models.VM) error
	GetVM(ctx context.Context, id string) (*models.VM, error)
	ListVMs(ctx context.Context) ([]*models.VM, error)
	DeleteVM(ctx context.Context, id string) error
	Close() error
}
