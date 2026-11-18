#!/bin/bash
# ==============================================================================
# AWS EC2 User Data Script for Minimal Two-Node Cloud Hypervisor VM Controller
#
# Usage:
#   Paste this script into the "User Data" field under "Advanced details"
#   when launching an Ubuntu 24.04 or 22.04 LTS EC2 instance.
#
# Output logs: /var/log/user-data.log
# ==============================================================================

set -euo pipefail

# Redirect stdout and stderr to syslog and user-data log
exec > >(tee -a /var/log/user-data.log | logger -t user-data -s 2>/dev/console) 2>&1
echo "=== Starting Cloud Hypervisor VM Controller EC2 Initialization ==="
echo "Date: $(date -u)"

export DEBIAN_FRONTEND=noninteractive

# 1. Update system packages
echo "--> Updating system packages..."
apt-get update -y
apt-get install -y --no-install-recommends \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg \
    git \
    ufw \
    jq \
    p7zip-full

if ! command -v aws &>/dev/null; then
    snap install aws-cli --classic 2>/dev/null || true
fi

# 2. Install Docker and Docker Compose v2
echo "--> Installing Docker Engine and Docker Compose..."
apt-get install -y docker.io docker-compose-v2
systemctl enable --now docker

# Add default ubuntu user to docker group
if id "ubuntu" &>/dev/null; then
    usermod -aG docker ubuntu
fi

# 3. Check for KVM hardware acceleration (/dev/kvm)
modprobe kvm 2>/dev/null || true
modprobe kvm_intel 2>/dev/null || true
HAS_KVM=false
if [ -e /dev/kvm ]; then
    echo "--> Hardware KVM detected at /dev/kvm. Configuring permissions..."
    chmod 666 /dev/kvm || true
    HAS_KVM=true
else
    echo "--> No /dev/kvm device detected (Standard virtualized EC2 instance)."
    echo "--> System will automatically run in Mock Hypervisor mode with real interactive PTY shells."
fi

# 4. Clone or pull repository to /opt/vm-controller
REPO_DIR="/opt/vm-controller"
REPO_URL="https://github.com/aiori-hackathon/A3-PS023-TC219.git"

echo "--> Setting up repository at ${REPO_DIR}..."
if [ ! -d "${REPO_DIR}" ]; then
    git clone "${REPO_URL}" "${REPO_DIR}"
else
    cd "${REPO_DIR}"
    git pull || true
fi

cd "${REPO_DIR}"

# 5. Calculate Host Memory & Public IP for .env
TOTAL_MEM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
TOTAL_MEM_MB=$((TOTAL_MEM_KB / 1024))
# Reserve 2048MB for Host & Controller, split remainder between 2 workers
WORKER_MEM=$(((TOTAL_MEM_MB - 2048) / 2))
if [ "$WORKER_MEM" -lt 2048 ]; then
    WORKER_MEM=2048
fi
echo "TOTAL_MEMORY_MB=${WORKER_MEM}" > .env
echo "--> Configured worker memory: ${WORKER_MEM} MB per worker node (Host RAM: ${TOTAL_MEM_MB} MB)"

TOKEN=$(curl -s -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60" 2>/dev/null || true)
if [ -n "${TOKEN}" ]; then
    PUBLIC_IP=$(curl -s -H "X-aws-ec2-metadata-token: ${TOKEN}" "http://169.254.169.254/latest/meta-data/public-ipv4" 2>/dev/null || true)
    if [ -n "${PUBLIC_IP}" ]; then
        echo "--> Detected AWS Public IPv4: ${PUBLIC_IP}"
        echo "HOST_IP=${PUBLIC_IP}" >> .env
    fi
fi

# 6. Optional: Sync OS images from S3 (configure S3_BUCKET at top if desired)
if [ -n "${S3_BUCKET:-}" ]; then
    echo "--> Syncing OS images from s3://${S3_BUCKET} to /opt/vm-data..."
    mkdir -p /opt/vm-data
    aws s3 sync "s3://${S3_BUCKET}" /opt/vm-data/ || true
fi

# 7. Configure host firewall (UFW)
echo "--> Configuring firewall rules..."
ufw allow 22/tcp comment 'EC2 Host SSH'
ufw allow 8080/tcp comment 'VM Controller Web Dashboard & API'
ufw allow 2201:2220/tcp comment 'Worker 1 VM SSH Bridges'
ufw allow 2301:2320/tcp comment 'Worker 2 VM SSH Bridges'
ufw --force enable || true

# 7. Build Docker images
echo "--> Building container images..."
docker compose build

# 8. Create systemd service for auto-start across instance reboots
echo "--> Creating systemd service..."
if [ "$HAS_KVM" = true ]; then
    START_CMD="/usr/bin/docker compose -f docker-compose.yml -f docker-compose.kvm.yml up -d"
else
    START_CMD="/usr/bin/docker compose up -d"
fi

cat <<EOF > /etc/systemd/system/vm-controller.service
[Unit]
Description=Minimal Two-Node Cloud Hypervisor VM Controller
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=${REPO_DIR}
ExecStart=${START_CMD}
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=180

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable vm-controller.service

# 9. Start services immediately
echo "--> Starting VM Controller cluster..."
systemctl start vm-controller.service

echo "=== VM Controller Cluster initialized successfully! ==="
echo "Web Dashboard: http://${PUBLIC_IP:-<your-instance-ip>}:8080"
echo "Check container status: docker compose ps"
