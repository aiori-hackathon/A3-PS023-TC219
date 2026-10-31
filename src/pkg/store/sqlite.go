package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"vm-controller/src/pkg/models"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a record is not found in the database.
var ErrNotFound = errors.New("record not found")

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSQLiteStore initializes SQLite database and creates the schema.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create sqlite db directory %s: %w", dir, err)
		}
	}

	dsn := dbPath
	if dbPath != ":memory:" {
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Controlled concurrency to prevent SQLite file locks
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{
		db: db,
	}

	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize sqlite schema: %w", err)
	}

	return s, nil
}

func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS vms (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		vcpus INTEGER NOT NULL,
		memory_mb INTEGER NOT NULL,
		state TEXT NOT NULL,
		host_id TEXT NOT NULL,
		ip_address TEXT NOT NULL DEFAULT '',
		os TEXT NOT NULL DEFAULT '',
		ssh_port INTEGER NOT NULL DEFAULT 0,
		serial_socket TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_vms_host_id ON vms(host_id);
	CREATE INDEX IF NOT EXISTS idx_vms_state ON vms(state);
	`
	_, err := s.db.Exec(schema)
	return err
}

// SaveVM atomically inserts or updates a VM record.
func (s *SQLiteStore) SaveVM(ctx context.Context, vm *models.VM) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	INSERT INTO vms (
		id, name, vcpus, memory_mb, state, host_id,
		ip_address, os, ssh_port, serial_socket, error_message,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		vcpus = excluded.vcpus,
		memory_mb = excluded.memory_mb,
		state = excluded.state,
		host_id = excluded.host_id,
		ip_address = excluded.ip_address,
		os = excluded.os,
		ssh_port = excluded.ssh_port,
		serial_socket = excluded.serial_socket,
		error_message = excluded.error_message,
		updated_at = excluded.updated_at;
	`

	createdUnix := vm.CreatedAt.UnixNano()
	updatedUnix := vm.UpdatedAt.UnixNano()
	if createdUnix == 0 {
		createdUnix = time.Now().UnixNano()
	}
	if updatedUnix == 0 {
		updatedUnix = time.Now().UnixNano()
	}

	_, err := s.db.ExecContext(ctx, query,
		vm.ID,
		vm.Name,
		vm.VCPUs,
		vm.MemoryMB,
		string(vm.State),
		vm.HostID,
		vm.IPAddress,
		vm.OS,
		vm.SSHPort,
		vm.SerialSocket,
		vm.ErrorMessage,
		createdUnix,
		updatedUnix,
	)
	return err
}

// GetVM retrieves a single VM by ID.
func (s *SQLiteStore) GetVM(ctx context.Context, id string) (*models.VM, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	SELECT id, name, vcpus, memory_mb, state, host_id,
	       ip_address, os, ssh_port, serial_socket, error_message,
	       created_at, updated_at
	FROM vms WHERE id = ?
	`
	row := s.db.QueryRowContext(ctx, query, id)

	var (
		vm          models.VM
		stateStr    string
		createdUnix int64
		updatedUnix int64
	)

	err := row.Scan(
		&vm.ID,
		&vm.Name,
		&vm.VCPUs,
		&vm.MemoryMB,
		&stateStr,
		&vm.HostID,
		&vm.IPAddress,
		&vm.OS,
		&vm.SSHPort,
		&vm.SerialSocket,
		&vm.ErrorMessage,
		&createdUnix,
		&updatedUnix,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	vm.State = models.VMState(stateStr)
	vm.CreatedAt = time.Unix(0, createdUnix)
	vm.UpdatedAt = time.Unix(0, updatedUnix)

	return &vm, nil
}

// ListVMs retrieves all stored VMs sorted by creation time.
func (s *SQLiteStore) ListVMs(ctx context.Context) ([]*models.VM, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	SELECT id, name, vcpus, memory_mb, state, host_id,
	       ip_address, os, ssh_port, serial_socket, error_message,
	       created_at, updated_at
	FROM vms ORDER BY created_at ASC
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*models.VM
	for rows.Next() {
		var (
			vm          models.VM
			stateStr    string
			createdUnix int64
			updatedUnix int64
		)
		if err := rows.Scan(
			&vm.ID,
			&vm.Name,
			&vm.VCPUs,
			&vm.MemoryMB,
			&stateStr,
			&vm.HostID,
			&vm.IPAddress,
			&vm.OS,
			&vm.SSHPort,
			&vm.SerialSocket,
			&vm.ErrorMessage,
			&createdUnix,
			&updatedUnix,
		); err != nil {
			return nil, err
		}
		vm.State = models.VMState(stateStr)
		vm.CreatedAt = time.Unix(0, createdUnix)
		vm.UpdatedAt = time.Unix(0, updatedUnix)
		result = append(result, &vm)
	}

	return result, rows.Err()
}

// DeleteVM removes a VM by ID.
func (s *SQLiteStore) DeleteVM(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM vms WHERE id = ?", id)
	return err
}

// Close closes the underlying SQLite database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
