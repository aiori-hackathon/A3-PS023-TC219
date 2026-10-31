# Minimal Two-Node Cloud Hypervisor VM Controller Prototype

[![Go Version](https://img.shields.io/badge/Go-1.24%2B-00ADD8?style=flat&logo=go)](https://golang.org)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com)
[![Cloud Hypervisor](https://img.shields.io/badge/Hypervisor-Cloud%20Hypervisor%20v54.0-orange)](https://github.com/cloud-hypervisor/cloud-hypervisor)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Hackathon](https://img.shields.io/badge/AIORI%20Hackathon%203-PS--023-purple)](A3-PS023-TC219_AIORI3_Presentation.pdf)

A production-grade, lightweight, authenticated, and resource-aware virtual machine management system for two independent hosts. Built in pure Go, the system features a **central controller**, **two worker agents**, an embedded **interactive Web Dashboard**, **SQLite ACID persistence**, **deterministic memory-aware placement**, and **direct serial console SSH bridging**.

---

## 📺 Project Resources

- **Demo Video:** [YouTube Video](https://www.youtube.com/watch?v=4qsTIsR8GiE)
- **Orientation & Architecture Walkthrough:** [YouTube Video](https://www.youtube.com/watch?v=lNd5x0wZsCU)
- **Official Presentation:** [`A3-PS023-TC219_AIORI3_Presentation.pdf`](A3-PS023-TC219_AIORI3_Presentation.pdf)
- **Problem Statement:** PS-023 (Minimal Two-Node Cloud Hypervisor VM Controller)
- **Team:** Team Disciples of GO / Team Episteme (A3-PS023-TC219)

---

## 🎯 Problem Statement & Design Objectives

Managing a small number of KVM virtualization hosts does not require heavyweight, resource-hungry orchestrators (e.g. OpenStack or full Kubernetes with KubeVirt). However, a minimalist controller must still reliably address:

1. **Minimal Orchestration Overhead:** Zero-dependency static Go binaries with near-instant boot and ~15 MB container footprints.
2. **Deterministic Memory Placement:** Strict memory-aware host selection enforcing a mandatory **512 MB reserve** and tie-breaking by fewest active VMs.
3. **Race Condition Prevention:** In-flight atomic memory reservations prevent concurrent VM requests from oversubscribing host RAM.
4. **Orphan & Leak Prevention:** Automatic garbage collection cleans up stale Unix domain sockets, leaked disk images, and zombie hypervisor processes on host recovery.
5. **ACID Persistence & Crash Recovery:** Controller and workers persist state to embedded SQLite databases in WAL mode. Active VMs survive controller or worker restarts and automatically rebind their interactive SSH bridges.
6. **Cross-Platform Readiness:** Out-of-the-box execution on **Linux, macOS, and Windows** via Docker Compose, dynamically selecting hardware KVM acceleration or a built-in mock hypervisor.
7. **End-to-End Security:** Bearer token authentication on all management endpoints, strict payload validation, and non-root execution.

---

## 🏗️ Architecture & Component Model

### System Architecture Diagram

```mermaid
flowchart TD
    %% Clients Layer
    subgraph Clients ["Clients & External Access"]
        Browser["Web Browser (UI: http://localhost:8080)"]
        APIClient["API Client / cURL (Bearer Token Auth)"]
        SSHClient["SSH Client (ssh minix@localhost -p 2201 / 2301)"]
    end

    %% Central Controller Container
    subgraph ControllerContainer ["Docker Container: vm-controller (Port 8080)"]
        direction TB
        HttpServer["HTTP Router & API Handler"]
        WebUI["Embedded Web Dashboard"]
        PlacementEngine["Memory-Aware Placement Engine (512MB Reserve & Tie-Breaker)"]
        ReservationLock["In-Flight Memory Reservation Mutex"]
        Reconciler["Background Health Reconciler Loop"]
        ControllerDB[("SQLite Store (controller.db)")]

        HttpServer --> WebUI
        HttpServer --> PlacementEngine
        PlacementEngine --> ReservationLock
        HttpServer --> Reconciler
        HttpServer --> ControllerDB
        Reconciler --> ControllerDB
    end

    %% Networking
    Clients -->|"HTTP :8080"| HttpServer
    SSHClient -.->|"TCP :2201-2220"| Worker1_SSH
    SSHClient -.->|"TCP :2301-2320"| Worker2_SSH

    subgraph DockerNetwork ["Internal Docker Bridge Network: vm-kvm-net"]
        HttpServer -->|"HTTP /vms, /health (worker-1:8081)"| Worker1_API
        HttpServer -->|"HTTP /vms, /health (worker-2:8082)"| Worker2_API
    end

    %% Worker 1 Container
    subgraph Worker1Container ["Docker Container: vm-worker-1 (Port 8081)"]
        direction TB
        Worker1_API["Worker Agent REST API (Port 8081)"]
        Worker1_SM["Deterministic VM State Machine"]
        Worker1_SSH["SSH Bridge Server (Ports 2201-2220)"]
        Worker1_Driver["Hypervisor Driver Abstraction"]
        Worker1_CH["Cloud Hypervisor v54.0"]
        Worker1_Mock["Mock Hypervisor Engine (Interactive Shell)"]
        Worker1_DB[("SQLite Store (worker.db)")]
        Worker1_Disks["VM Disks & Unix Sockets (/data)"]

        Worker1_API --> Worker1_SM
        Worker1_SM --> Worker1_Driver
        Worker1_Driver --> Worker1_CH
        Worker1_Driver --> Worker1_Mock
        Worker1_API --> Worker1_DB
        Worker1_SSH -->|"Bridge to serial.sock"| Worker1_Disks
        Worker1_Driver --> Worker1_Disks
    end

    %% Worker 2 Container
    subgraph Worker2Container ["Docker Container: vm-worker-2 (Port 8082)"]
        direction TB
        Worker2_API["Worker Agent REST API (Port 8082)"]
        Worker2_SM["Deterministic VM State Machine"]
        Worker2_SSH["SSH Bridge Server (Ports 2301-2320)"]
        Worker2_Driver["Hypervisor Driver Abstraction"]
        Worker2_CH["Cloud Hypervisor v54.0"]
        Worker2_Mock["Mock Hypervisor Engine (Interactive Shell)"]
        Worker2_DB[("SQLite Store (worker.db)")]
        Worker2_Disks["VM Disks & Unix Sockets (/data)"]

        Worker2_API --> Worker2_SM
        Worker2_SM --> Worker2_Driver
        Worker2_Driver --> Worker2_CH
        Worker2_Driver --> Worker2_Mock
        Worker2_API --> Worker2_DB
        Worker2_SSH -->|"Bridge to serial.sock"| Worker2_Disks
        Worker2_Driver --> Worker2_Disks
    end

    %% Host Virtualization Layer
    subgraph HostVirtualization ["Underlying Host Execution Layer"]
        KVM["Linux Hardware KVM (/dev/kvm) - with docker-compose.kvm.yml"]
        CrossPlatform["macOS / Windows / Non-KVM Linux - standard docker-compose.yml"]
    end

    Worker1_CH --> KVM
    Worker2_CH --> KVM
    Worker1_Mock --> CrossPlatform
    Worker2_Mock --> CrossPlatform
```

### VM Placement & Lifecycle Execution Flow

```mermaid
sequenceDiagram
    autonumber
    actor User as User / Web UI
    participant C as Controller (vm-controller)
    participant DB as SQLite DB
    participant W1 as Worker 1 (vm-worker-1)
    participant W2 as Worker 2 (vm-worker-2)
    participant SSH as SSH Bridge

    User->>C: POST /vms (Create Request: 1-4 vCPUs, 512MB RAM)
    C->>W1: GET /resources (Query Capacity & Active VMs)
    W1-->>C: Available RAM, Reserve (512MB), Running VMs
    C->>W2: GET /resources (Query Capacity & Active VMs)
    W2-->>C: Available RAM, Reserve (512MB), Running VMs

    Note over C: Placement Algorithm: Evaluates max(Free - 512MB)<br/>Breaks ties by fewest running VMs
    C->>C: Acquire In-Flight Reservation Lock
    C->>DB: Save VM in CREATING State
    C->>W1: POST /vms (Forward Create to Selected Host)
    W1->>W1: Allocate Disk, Unix Socket & IP
    W1->>W1: Save in Worker SQLite
    W1-->>C: 200 OK (VM Created)
    C->>DB: Update VM to CREATED State
    C-->>User: 200 OK (VM in CREATED State)

    User->>C: POST /vms/{id}/start
    C->>W1: POST /vms/{id}/start
    W1->>W1: Boot Hypervisor (Cloud Hypervisor or Mock)
    W1->>SSH: Bind SSH Bridge on Port 2201 -> serial.sock
    W1-->>C: 200 OK (VM RUNNING, SSH Port 2201)
    C->>DB: Update VM to RUNNING State
    C-->>User: 200 OK (VM RUNNING)

    User->>SSH: ssh -p 2201 minix@localhost
    SSH->>W1: Connect to serial.sock
    W1-->>User: Interactive MINIX 3 Console (minix# )
```

### 1. Central Controller (`src/cmd/controller`)
- Exposes authenticated REST endpoints and serves the embedded single-page responsive dashboard.
- Evaluates candidate workers based on available RAM, subtracts the 512 MB safety reserve, breaks ties by fewest active VMs, or honors explicit host selection.
- Background reconciler loop monitors worker heartbeats, recovers state from workers, and marks unreachable instances as `ERROR`.
- Persists cluster topology in SQLite (`controller.db` in WAL mode).

### 2. Worker Agents (`src/cmd/worker`)
- Manages hypervisor execution, disk image allocation, and process lifecycles.
- Enforces strict deterministic state machine: `CREATING` -> `CREATED` -> `RUNNING` -> `STOPPED` -> `DELETED` (with `ERROR` handling).
- Integrates both real **Cloud Hypervisor v54.0** (via Unix socket JSON-RPC) and a **Mock Hypervisor** featuring a complete interactive MINIX 3 serial shell.
- Spins up local SSH server endpoints bridging incoming terminal sessions directly to VM serial sockets (`serial.sock`).
- Persists local VM registry in SQLite (`worker.db` in WAL mode).

---

## 🚀 Quick Start with Docker (Recommended)

The entire cluster runs in separate, isolated containers orchestrated with Docker Compose. It works out-of-the-box on **macOS (Apple Silicon & Intel), Windows (Docker Desktop / WSL2), and Linux**.

### Service Topology

| Service | Container Name | Host Port Mapping | Description |
|---|---|---|---|
| **Controller** | `vm-controller` | `8080:8080` | Web Dashboard & Central Orchestration API |
| **Worker 1** | `vm-worker-1` | `8081:8081`<br>`2201-2220:2201-2220` | Worker Node 1 Agent & Guest SSH Bridges |
| **Worker 2** | `vm-worker-2` | `8082:8082`<br>`2301-2320:2301-2320` | Worker Node 2 Agent & Guest SSH Bridges |

### 1. Run on Any Operating System (macOS, Windows, or Linux)
On any host without requiring hardware virtualization, the cluster uses `--driver=auto` to automatically boot with the Mock Hypervisor:
```bash
docker compose up -d
```

### 2. Run on Linux with Hardware KVM Acceleration
On Linux hosts with `/dev/kvm` hardware virtualization enabled, include the KVM overlay to pass `/dev/kvm` directly to the worker nodes and invoke real Cloud Hypervisor v54.0:
```bash
docker compose -f docker-compose.yml -f docker-compose.kvm.yml up -d
```

### 3. Verify Cluster Health
```bash
docker compose ps
```
All three containers (`vm-controller`, `vm-worker-1`, `vm-worker-2`) will report `Up (healthy)`.

### 4. Access the Web Dashboard
Open your web browser at:
```
http://localhost:8080
```

### 5. Access VM Serial Console via SSH
Each running VM automatically exposes an SSH server bridging directly to the guest console. Simply run:
```bash
# Connect to a VM running on Worker 1
ssh -o StrictHostKeyChecking=no minix@localhost -p 2201

# Connect to a VM running on Worker 2
ssh -o StrictHostKeyChecking=no minix@localhost -p 2301
```
*(Accepts any password or empty password; immediately drops into the interactive MINIX 3 shell!)*

### 6. Restart & Persistence Test
All VM records and controller registrations are saved to SQLite databases residing on Docker volumes (`vm-controller-data`, `vm-worker1-data`, `vm-worker2-data`). You can restart containers at any time without losing active VMs:
```bash
docker compose restart
```

### 7. Teardown
```bash
# Stop containers (preserves all data in volumes)
docker compose stop

# Destroy containers and network (volumes preserved)
docker compose down

# Destroy containers and wipe all volume data
docker compose down -v
```

---

## 💻 Local / Bare-Metal Execution

If you prefer to compile and run directly on your host:

### 1. Run Test Suite
```bash
go test -v ./...
```

### 2. Start Worker 1 (Port 8081)
```bash
go run ./src/cmd/worker \
  -id=worker-1 \
  -addr=127.0.0.1:8081 \
  -token=secret-worker-key \
  -memory-mb=4096 \
  -driver=auto
```

### 3. Start Worker 2 (Port 8082)
```bash
go run ./src/cmd/worker \
  -id=worker-2 \
  -addr=127.0.0.1:8082 \
  -token=secret-worker-key \
  -memory-mb=4096 \
  -driver=auto
```

### 4. Start the Central Controller (Port 8080)
```bash
go run ./src/cmd/controller \
  -addr=127.0.0.1:8080 \
  -token=secret-controller-key \
  -worker1-id=worker-1 -worker1-url=http://127.0.0.1:8081 -worker1-token=secret-worker-key \
  -worker2-id=worker-2 -worker2-url=http://127.0.0.1:8082 -worker2-token=secret-worker-key
```

---

## 🖥️ Web Dashboard Features

Access the dashboard at `http://localhost:8080/`:

1. **Cluster Overview Cards:** Real-time metrics for Worker 1 and Worker 2 (RAM capacity, reserve, free memory, and active VM count) rendered in stable, deterministic order.
2. **Explicit Worker Selection:** Deploy VMs to `Auto` (memory-aware placement), or specifically designate `worker-1` or `worker-2`.
3. **Cluster Filtering:** Filter the VM list to inspect all nodes or isolate a specific worker.
4. **Live Resource Telemetry:** Real-time visual progress bars display CPU % and physical memory RSS usage per virtual machine.
5. **Instant SSH Connection Tag:** Click the **[SSH]** tag on any running VM to copy the exact `ssh -o StrictHostKeyChecking=no minix@<host> -p <port>` connection string.
6. **One-Click Lifecycle Actions:** Direct buttons for `Start`, `Stop`, and `Delete`, with safety checks and error banners.

---

## 📡 REST API Reference

All protected endpoints require the HTTP header:
```http
Authorization: Bearer <token>
```
Default tokens:
- Controller Token: `secret-controller-key`
- Worker Token: `secret-worker-key`

### Summary of Endpoints

| Method | Endpoint | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | No | Health check status (`{"role":"controller","status":"ok"}`) |
| `GET` | `/workers` | Yes | List registered worker nodes, health, and memory capacity |
| `POST` | `/vms` | Yes | Create a new VM (triggers memory-aware placement) |
| `GET` | `/vms` | Yes | List all VMs across the cluster |
| `GET` | `/vms/{id}` | Yes | Retrieve status, metrics, and network info for a specific VM |
| `POST` | `/vms/{id}/start` | Yes | Start VM (`CREATED`/`STOPPED` -> `RUNNING`) and open SSH bridge |
| `POST` | `/vms/{id}/stop` | Yes | Stop VM (`RUNNING` -> `STOPPED`) and teardown SSH bridge |
| `DELETE` | `/vms/{id}` | Yes | Delete VM and reclaim disk, socket, and memory resources |

### Example API Requests

#### 1. Create a VM (Auto Placement)
```bash
curl -s -X POST http://localhost:8080/vms \
  -H "Authorization: Bearer secret-controller-key" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "production-api",
    "vcpus": 2,
    "memory_mb": 512
  }'
```

#### 2. Create a VM on a Specific Worker
```bash
curl -s -X POST http://localhost:8080/vms \
  -H "Authorization: Bearer secret-controller-key" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "worker2-cache",
    "vcpus": 1,
    "memory_mb": 1024,
    "worker_id": "worker-2"
  }'
```

#### 3. Start a VM
```bash
curl -s -X POST http://localhost:8080/vms/vm-1/start \
  -H "Authorization: Bearer secret-controller-key"
```

#### 4. Stop a VM
```bash
curl -s -X POST http://localhost:8080/vms/vm-1/stop \
  -H "Authorization: Bearer secret-controller-key"
```

#### 5. Delete a VM
```bash
curl -s -X DELETE http://localhost:8080/vms/vm-1 \
  -H "Authorization: Bearer secret-controller-key"
```

---

## ⚖️ VM Constraints & Placement Rules

| Resource | Constraint / Limit |
|---|---|
| **vCPUs** | 1–4 vCPUs per virtual machine |
| **Memory** | 128 MB – 2048 MB per virtual machine |
| **Worker Reserved Memory** | 512 MB reserved exclusively for host OS / hypervisor overhead |
| **Placement Algorithm** | Selects healthy worker with `max(free_memory - 512MB)` |
| **Tie-Breaking Rule** | If free memory is identical, selects worker hosting fewer running VMs |
| **Insufficient Memory** | Rejects request with `HTTP 507 Insufficient Storage` |

---

## 📂 Project Repository Structure

```
.
├── Dockerfile                  # Multi-stage Dockerfile (builds controller & worker)
├── Dockerfile.controller       # Standalone Dockerfile for Controller
├── Dockerfile.worker           # Standalone Dockerfile for Worker Agent
├── docker-compose.yml          # Base multi-container Compose orchestration
├── docker-compose.kvm.yml      # Linux KVM hardware acceleration Compose override
├── .dockerignore               # Optimized Docker build context filter
├── .gitignore                  # Git ignore rules
├── go.mod                      # Go module definition
├── go.sum                      # Go dependency checksums
├── README.md                   # Unified project documentation (this file)
├── A3-PS023-TC219_...pdf       # AIORI-3 Presentation Slide Deck
└── src/
    ├── cmd/
    │   ├── controller/         # Central Controller main entrypoint
    │   └── worker/             # Worker Agent main entrypoint
    └── pkg/
        ├── config/             # Environment variable and CLI flag helpers
        ├── controller/         # Controller logic, placement engine, reconciler, and UI
        ├── models/             # Data structs, state machine validator, API models
        ├── store/              # SQLite ACID persistence engine (WAL mode)
        └── worker/             # Worker agent, Cloud & Mock hypervisors, SSH bridge
```

---

## 🛠️ Technology Stack

| Layer | Technology | Purpose |
|---|---|---|
| **Language** | Go (Golang) 1.24+ | Zero-CGO, fast, cross-platform static compilation |
| **Database** | Embedded SQLite (`modernc.org/sqlite`) | ACID transactional persistence in WAL mode |
| **Hypervisor** | Cloud Hypervisor v54.0 | Modern Rust-based KVM micro-hypervisor |
| **Mock Hypervisor** | Built-in Pure Go Engine | Interactive MINIX 3 shell emulation for non-Linux hosts |
| **Serial Bridging** | `golang.org/x/crypto/ssh` | Terminal PTY bridging over Unix domain sockets |
| **Networking** | Docker Bridge (`vm-kvm-net`) | Internal container DNS routing and external port publishing |
| **Resource Control** | Linux cgroups v2 / `systemd-run` | CPU quota and memory limits |

---

## 🗓️ Deployment Timeline & Milestones

| Milestone | Status | Description |
|---|---|---|
| **M0** | Completed | Environment setup, static binary compilation, manual Cloud Hypervisor boot |
| **M1** | Completed | Worker agent core, VM lifecycle state machine, and process supervision |
| **M2** | Completed | Worker REST API, Bearer token authentication, and resource teardown |
| **M3** | Completed | Central Controller REST API, VM registry, and embedded Web Dashboard |
| **M4** | Completed | Memory-aware placement engine, 512 MB reserve, and concurrent race locking |
| **M5** | Completed | Periodic reconciler loop, crash detection, worker recovery, and SQLite persistence |
| **M6** | Completed | Comprehensive unit and integration test suite (`go test -v ./...`) |
| **M7** | Completed | Full Docker containerization, cross-platform Compose, and unified documentation |

---

## 👥 Team Composition

| Name | Role | Responsibilities |
|---|---|---|
| **Hrittik Biswas** | Controller Lead | Central controller, scheduling algorithms, and Web UI |
| **Reek Saha** | Worker Agent Lead | Worker agents, hypervisor integration, and SSH bridging |
| **Joy Sammandar** | Assistant Professor | Academic mentorship and project review |

---

## 📚 References & Standards

1. **Cloud Hypervisor Documentation:** [https://github.com/cloud-hypervisor/cloud-hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor)
2. **Cloud Hypervisor API Specification:** [https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/api.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/api.md)
3. **Linux KVM Kernel API:** [https://www.kernel.org/doc/html/latest/virt/kvm/api.html](https://www.kernel.org/doc/html/latest/virt/kvm/api.html)
4. **RFC 6750 — Bearer Token Usage:** [https://www.rfc-editor.org/rfc/rfc6750.html](https://www.rfc-editor.org/rfc/rfc6750.html)

---

**Team Disciples of GO**  
*AIORI Hackathon 3 · Cloud Computing Track · PS-023*  
*A3-PS023-TC219*
