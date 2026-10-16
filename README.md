# AIORI Hackathon 3 — Team Episteme A3-PS023-TC219

## PS-023: Minimal Two-Node Cloud Hypervisor VM Controller

A lightweight, authenticated, and resource-aware virtual machine management system for two independent Linux KVM hosts using Cloud Hypervisor.

The proposed system consists of one central controller and two worker agents. It provides a unified interface for VM lifecycle management, memory-aware host placement, resource limits, and failure cleanup.

---

## 📺 Team Introduction & Orientation Video

Watch our introductory walkthrough explaining the team, problem statement, and proposed system.

- **YouTube:** https://www.youtube.com/watch?v=lNd5x0wZsCU

---

## 🎯 Problem Statement

Managing a small number of KVM hosts does not necessarily require a heavyweight orchestration platform. However, a lightweight VM controller must still address resource allocation, security, lifecycle consistency, and failure recovery.

The PS-023 problem focuses on the following challenges:

- **Orchestration overhead:** Avoid unnecessary components when managing two KVM hosts and a small number of virtual machines.
- **Orphaned processes:** Prevent VM processes, Unix sockets, TAP devices, and temporary disk files from remaining after failures.
- **Memory contention:** Prevent concurrent VM creation requests from overcommitting host memory.
- **Resource limits:** Enforce CPU and memory limits at the host level.
- **Partial failures:** Clean up temporary resources and roll back allocations when VM creation or startup fails.
- **Security:** Authenticate API requests to prevent unauthorized VM operations.

---

## 🏗️ Proposed Solution

The system uses a central controller and two worker agents, with one worker assigned to each KVM-capable host.

### System Components

**1. Central Controller**
- Exposes the REST API for VM management.
- Authenticates and validates requests.
- Selects a suitable host using memory-aware placement.
- Maintains VM records and coordinates lifecycle operations.
- Tracks worker health and handles retries.

**2. Worker Agents**
- Execute VM operations on their respective hosts.
- Prepare per-VM disk copies and handle TAP/IP allocation.
- Start and manage Cloud Hypervisor processes.
- Communicate with the local hypervisor through its Unix domain socket.
- Report VM status and errors to the controller.

**3. Cloud Hypervisor and Linux KVM**
- Cloud Hypervisor performs the virtual machine management operations.
- Linux KVM provides the underlying virtualization support on each host.
- Both hosts must support `/dev/kvm` and have the required networking configured.

---
## 🛠️ Technology Stack

| Component | Technology |
|---|---|
| Programming language | Go (Golang) |
| HTTP API | Go standard library `net/http` |
| HTTP client | Go `net/http` client |
| Virtualization | Cloud Hypervisor |
| Host virtualization | Linux KVM |
| Hypervisor communication | Unix domain sockets |
| VM records | SQLite |
| Resource control | Linux cgroups v2 |
| API authentication | Bearer token authentication |
| Worker communication | Authenticated REST API |
| Process management | Go `os/exec` and Linux process groups |
| Concurrency | Go goroutines and synchronization primitives |

### Go Implementation

- **Controller:** Handles API requests, authentication, VM records, host selection, memory reservations, and worker coordination.
- **Worker agents:** Manage local VM operations, Cloud Hypervisor processes, disk copies, TAP/IP allocation, and cleanup.
- **Hypervisor integration:** Communicates with Cloud Hypervisor through its Unix domain socket API.
- **Resource management:** Uses Linux host resource information and cgroups v2 to support memory-aware placement and CPU/memory limits.
- **Reliability:** Uses Go's concurrency primitives to coordinate operations and synchronization around shared resource reservations.

---

## ⚙️ Core Features

### 1. VM Lifecycle Management

The controller follows a deterministic VM lifecycle:

`CREATING` → `CREATED` → `RUNNING` → `STOPPED` → `DELETED`

- Create, start, stop, delete, and list virtual machines.
- Permit only valid state transitions.
- Return HTTP `409 Conflict` for invalid transitions.
- Support idempotent lifecycle operations where specified.
- Roll back failed operations and reclaim resources.

### 2. Memory-Aware Placement

The controller collects resource information from both workers before selecting a host.

- Select a healthy host with sufficient available memory.
- Maintain a 512 MB memory reserve when evaluating placement.
- Prefer the host running fewer VMs when eligible hosts have equal placement priority.
- Reserve memory before dispatching VM creation requests.
- Reject placement when neither host can satisfy the request.

### 3. Authentication and Validation

- Authenticate controller and worker API requests.
- Validate VM resource requirements before launching a VM.
- Use Bearer token authentication for protected API operations.
- Avoid using `shell=True` for system operations.

### 4. Resource Isolation

- Run hypervisor processes in separate process groups.
- Use cgroups v2 to apply configured CPU and memory limits.
- Validate resource availability before VM placement.

### 5. Reliability and Cleanup

- Monitor worker health using heartbeats.
- Reconcile VM records with worker state.
- Use bounded retries and idempotency.
- Handle worker unavailability and hypervisor failures.
- Remove orphaned processes and temporary resources where applicable.
- Release reserved memory after failed operations.

---

## 📊 VM Resource Constraints

| Resource | Configured constraint |
|---|---|
| vCPU | 1–4 |
| VM memory | 128–2048 MB |
| Memory reserve | 512 MB |
| Host selection | Healthy host with sufficient free memory |

Placement also depends on current host resource availability.

---

## 🗓️ Deployment Timeline

The project plan is organized into eight milestones:

| Milestone | Focus |
|---|---|
| M0 | Environment setup and manual VM boot |
| M1 | Worker core and VM lifecycle operations |
| M2 | Worker REST API, authentication, and teardown |
| M3 | Controller API, VM records, and state management |
| M4 | Memory-aware placement and resource limits |
| M5 | Failure handling, reconciliation, and retries |
| M6 | Testing and resource leak checks |
| M7 | Report, demonstration, and repository handover |

The methodology emphasizes manual-first learning, separate controller and worker development, peer review, and early integration testing.

---

## 👥 Team Composition

| Name | Role | Work area |
|---|---|---|
| **Soham Chakraborti** | Controller Lead | Controller and scheduling |
| **Ishita Roy** | Worker Agent Lead | Worker agents and host-level VM operations |
| **John Doe** | Faculty Member · Assistant Professor | Faculty designation shown on the presentation's Thank You slide |

---

## 📚 References

The project references the following documentation and standards:

1. **Cloud Hypervisor API:** https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/api.md
2. **Linux KVM API Documentation:** https://www.kernel.org/doc/html/latest/virt/kvm/api.html
3. **Cloud Hypervisor Project:** https://github.com/cloud-hypervisor/cloud-hypervisor
4. **RFC 6750 — Bearer Token Usage:** https://www.rfc-editor.org/rfc/rfc6750.html

---

## 📌 Scope and Non-Goals

### In Scope
- One central controller and two worker agents.
- VM lifecycle management across two KVM hosts.
- Memory-aware host selection and reservations.
- Authenticated API communication.
- CPU and memory limits.
- Failure handling and resource cleanup.

### Explicit Non-Goals
- Live VM migration.
- Distributed storage.
- Browser-based VNC.
- VM image marketplace.
- Developing a new hypervisor.

---

## 📄 Project Presentation

The AIORI-3 presentation contains the problem description, background, proposed solution, optimization strategies, architecture, data flow, deployment timeline, literature and RFC review, and bibliography.

**Presentation file:** `A3-PS023-TC213_AIORI3_Presentation.pdf`

---

**Team Disciples of GO**  
AIORI Hackathon 3 · Cloud Computing · PS-023  
A3-PS023-TC219
