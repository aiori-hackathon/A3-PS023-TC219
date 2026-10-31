package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"vm-controller/src/pkg/config"
	"vm-controller/src/pkg/worker"
)

func main() {
	var (
		id           = flag.String("id", config.EnvString("WORKER_ID", "worker-1"), "Worker identifier")
		addr         = flag.String("addr", config.EnvString("WORKER_ADDR", "127.0.0.1:8081"), "HTTP listen address")
		token        = flag.String("token", config.EnvString("WORKER_TOKEN", "secret-worker-key"), "Bearer auth token")
		driverType   = flag.String("driver", config.EnvString("HYPERVISOR_DRIVER", "auto"), "Hypervisor driver: auto, mock, or cloud-hypervisor")
		memMB        = flag.Int("memory-mb", config.EnvInt("TOTAL_MEMORY_MB", 4096), "Total memory capacity in MB")
		dataDir      = flag.String("data-dir", config.EnvString("DATA_DIR", ""), "Base directory for VM files and sockets")
		chBin        = flag.String("ch-bin", config.EnvString("CH_BIN", "cloud-hypervisor"), "Path to cloud-hypervisor binary")
		kernelPath   = flag.String("kernel", config.EnvString("KERNEL_PATH", ""), "Path to guest kernel")
		baseDiskPath = flag.String("base-disk", config.EnvString("BASE_DISK_PATH", ""), "Path to base rootfs disk image")
	)
	flag.Parse()

	if *dataDir == "" {
		*dataDir = filepath.Join(os.TempDir(), "vm-worker-"+*id)
	}

	var driver worker.HypervisorDriver
	useCH := *driverType == "cloud-hypervisor"
	if *driverType == "auto" {
		if _, err := exec.LookPath(*chBin); err == nil {
			if _, err := os.Stat("/dev/kvm"); err == nil {
				useCH = true
			}
		}
	}

	if useCH {
		log.Printf("[Worker %s] Initializing Cloud Hypervisor driver (bin: %s, data: %s)", *id, *chBin, *dataDir)
		driver = worker.NewCloudHypervisorDriver(*dataDir, *chBin, *kernelPath, *baseDiskPath)
	} else {
		log.Printf("[Worker %s] Initializing Mock Hypervisor driver (data: %s)", *id, *dataDir)
		driver = worker.NewMockHypervisor(*dataDir)
	}

	w := worker.NewWorker(worker.Config{
		ID:               *id,
		Token:            *token,
		TotalMemoryMB:    *memMB,
		ReservedMemoryMB: 512,
		Driver:           driver,
		DataDir:          *dataDir,
	})
	defer w.Close()

	handler := worker.NewHandler(w)
	server := &http.Server{
		Addr:    *addr,
		Handler: handler,
	}

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[Worker %s] Listening on %s (Memory: %d MB)", *id, *addr, *memMB)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Worker %s] Server error: %v", *id, err)
		}
	}()

	<-stopCh
	log.Printf("[Worker %s] Shutting down...", *id)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Worker %s] Error shutting down: %v", *id, err)
	}
	log.Printf("[Worker %s] Stopped cleanly", *id)
}
