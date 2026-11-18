#!/bin/bash
# ==============================================================================
# OS Image Preparation Helper for Cloud Hypervisor
#
# Extracts kernel and initramfs from a Linux ISO, or converts cloud images
# for use with Cloud Hypervisor.
#
# Usage:
#   ./scripts/prepare-os-image.sh /path/to/linux.iso [/output/directory]
# ==============================================================================

set -euo pipefail

ISO_PATH="${1:-}"
OUTPUT_DIR="${2:-/opt/vm-data}"

if [ -z "$ISO_PATH" ] || [ ! -f "$ISO_PATH" ]; then
    echo "Usage: $0 <path-to-iso-file> [output-dir]"
    echo "Example: $0 /opt/vm-data/ubuntu-24.04.iso /opt/vm-data"
    exit 1
fi

mkdir -p "$OUTPUT_DIR"
TMP_DIR=$(mktemp -d /tmp/iso-extract-XXXXXX)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "--> Inspecting and extracting $ISO_PATH..."
if command -v 7z &>/dev/null; then
    7z x "$ISO_PATH" -o"$TMP_DIR" -y > /dev/null
else
    mkdir -p "$TMP_DIR/mount"
    mount -o loop "$ISO_PATH" "$TMP_DIR/mount"
    cp -r "$TMP_DIR/mount"/* "$TMP_DIR/" || true
    umount "$TMP_DIR/mount" || true
fi

# Locate kernel (vmlinuz*)
KERNEL_FOUND=$(find "$TMP_DIR" -type f -name "vmlinuz*" | head -n 1 || true)
# Locate initramfs/initrd
INITRD_FOUND=$(find "$TMP_DIR" -type f \( -name "initrd*" -o -name "initramfs*" \) | head -n 1 || true)

if [ -n "$KERNEL_FOUND" ]; then
    cp "$KERNEL_FOUND" "$OUTPUT_DIR/vmlinuz"
    chmod 644 "$OUTPUT_DIR/vmlinuz"
    echo "✔ Found kernel: copied to $OUTPUT_DIR/vmlinuz"
else
    echo "⚠ No vmlinuz file found in ISO. Ensure this is a bootable Linux x86_64 ISO."
fi

if [ -n "$INITRD_FOUND" ]; then
    cp "$INITRD_FOUND" "$OUTPUT_DIR/initramfs"
    chmod 644 "$OUTPUT_DIR/initramfs"
    echo "✔ Found initramfs: copied to $OUTPUT_DIR/initramfs"
else
    echo "⚠ No initrd/initramfs found in ISO."
fi

echo ""
echo "=== Summary ==="
echo "Artifacts placed in: $OUTPUT_DIR"
ls -lh "$OUTPUT_DIR"
