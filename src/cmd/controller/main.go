package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vm-controller/src/pkg/config"
	"vm-controller/src/pkg/controller"
)

func main() {
	var (
		addr         = flag.String("addr", config.EnvString("CONTROLLER_ADDR", "127.0.0.1:8080"), "HTTP listen address")
		token        = flag.String("token", config.EnvString("CONTROLLER_TOKEN", "secret-controller-key"), "Bearer auth token for clients")
		dataDir      = flag.String("data-dir", config.EnvString("CONTROLLER_DATA_DIR", ""), "Base directory for controller state")
		worker1ID    = flag.String("worker1-id", config.EnvString("WORKER_1_ID", "worker-1"), "ID of first worker")
		worker1URL   = flag.String("worker1-url", config.EnvString("WORKER_1_URL", "http://127.0.0.1:8081"), "URL of first worker")
		worker1Token = flag.String("worker1-token", config.EnvString("WORKER_1_TOKEN", "secret-worker-key"), "Bearer token for worker 1")
		worker2ID    = flag.String("worker2-id", config.EnvString("WORKER_2_ID", "worker-2"), "ID of second worker")
		worker2URL   = flag.String("worker2-url", config.EnvString("WORKER_2_URL", "http://127.0.0.1:8082"), "URL of second worker")
		worker2Token = flag.String("worker2-token", config.EnvString("WORKER_2_TOKEN", "secret-worker-key"), "Bearer token for worker 2")
	)
	flag.Parse()

	ctrl := controller.NewController(controller.Config{
		Addr:    *addr,
		Token:   *token,
		DataDir: *dataDir,
		Workers: []controller.WorkerTarget{
			{ID: *worker1ID, URL: *worker1URL, Token: *worker1Token},
			{ID: *worker2ID, URL: *worker2URL, Token: *worker2Token},
		},
	})
	defer ctrl.Close()

	handler := controller.NewHandler(ctrl)
	server := &http.Server{
		Addr:    *addr,
		Handler: handler,
	}

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[Controller] Listening on %s (Workers: %s at %s, %s at %s)",
			*addr, *worker1ID, *worker1URL, *worker2ID, *worker2URL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Controller] Server error: %v", err)
		}
	}()

	<-stopCh
	log.Printf("[Controller] Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Controller] Error shutting down: %v", err)
	}
	log.Printf("[Controller] Stopped cleanly")
}
