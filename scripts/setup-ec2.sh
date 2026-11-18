#!/bin/bash
# ==============================================================================
# Manual Host Setup Script for AWS EC2 Instances
#
# Run this script on your EC2 instance if you launched it without User Data:
#   sudo ./scripts/setup-ec2.sh
# ==============================================================================

set -euo pipefail

if [ "$EUID" -ne 0 ]; then
    echo "Error: Please run as root (sudo ./scripts/setup-ec2.sh)"
    exit 1
fi

echo "=== Setting up VM Controller on EC2 Instance ==="

# 1. Install prerequisites
apt-get update -y
apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg git ufw jq docker.io docker-compose-v2 p7zip-full

# Install AWS CLI via snap on Ubuntu 24.04 if missing
if ! command -v aws &>/dev/null; then
    snap install aws-cli --classic 2>/dev/null || true
fi

systemctl enable --now docker

# Add invoking user to docker group
TARGET_USER="${SUDO_USER:-ubuntu}"
if id "$TARGET_USER" &>/dev/null; then
    usermod -aG docker "$TARGET_USER"
    echo "Added $TARGET_USER to docker group."
fi

# 2. Check KVM
if [ -e /dev/kvm ]; then
    echo "Hardware /dev/kvm found. Setting permissions..."
    chmod 666 /dev/kvm
    COMPOSE_ARGS="-f docker-compose.yml -f docker-compose.kvm.yml"
else
    echo "No /dev/kvm found. Running in mock hypervisor mode."
    COMPOSE_ARGS="-f docker-compose.yml"
fi

# 3. Detect Public IP & Calculate Worker Memory
TOTAL_MEM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
TOTAL_MEM_MB=$((TOTAL_MEM_KB / 1024))
WORKER_MEM=$(((TOTAL_MEM_MB - 2048) / 2))
if [ "$WORKER_MEM" -lt 2048 ]; then
    WORKER_MEM=2048
fi
echo "TOTAL_MEMORY_MB=${WORKER_MEM}" > .env
echo "Allocating ${WORKER_MEM} MB RAM per worker node (Host RAM: ${TOTAL_MEM_MB} MB)."

TOKEN=$(curl -s -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60" 2>/dev/null || true)
PUBLIC_IP=""
if [ -n "${TOKEN}" ]; then
    PUBLIC_IP=$(curl -s -H "X-aws-ec2-metadata-token: ${TOKEN}" "http://169.254.169.254/latest/meta-data/public-ipv4" 2>/dev/null || true)
    if [ -n "${PUBLIC_IP}" ]; then
        echo "HOST_IP=${PUBLIC_IP}" >> .env
    fi
fi

# 3.5 Optional: Sync OS images from S3 if passed as argument or env var
S3_BUCKET="${1:-${S3_BUCKET:-}}"
if [ -n "${S3_BUCKET}" ]; then
    # Strip s3:// prefix if user passed s3://my-bucket
    S3_BUCKET_NAME="${S3_BUCKET#s3://}"
    echo "Syncing images from s3://${S3_BUCKET_NAME} to /opt/vm-data..."
    mkdir -p /opt/vm-data
    aws s3 sync "s3://${S3_BUCKET_NAME}" /opt/vm-data/ || true
fi

# 4. Open Firewall Ports
ufw allow 22/tcp comment 'SSH'
ufw allow 8080/tcp comment 'Dashboard'
ufw allow 2201:2220/tcp comment 'Worker-1 SSH'
ufw allow 2301:2320/tcp comment 'Worker-2 SSH'
ufw --force enable || true

# 5. Build and Start Containers
cd "$(dirname "$0")/.."
echo "Building and launching containers..."
docker compose build
docker compose ${COMPOSE_ARGS} up -d

echo ""
echo "=========================================================="
echo " Setup complete!"
if [ -n "$PUBLIC_IP" ]; then
    echo " Dashboard: http://${PUBLIC_IP}:8080"
    echo " SSH to VMs: ssh -o StrictHostKeyChecking=no minix@${PUBLIC_IP} -p 2201"
else
    echo " Dashboard: http://<EC2-PUBLIC-IP>:8080"
    echo " SSH to VMs: ssh -o StrictHostKeyChecking=no minix@<EC2-PUBLIC-IP> -p 2201"
fi
echo "=========================================================="
