#!/usr/bin/env bash
# setup.sh brings up the infrastructure the Layer 2 (e2e) test asserts against:
# a single-node k3s cluster, the provider image built and imported into that
# cluster, the upstream Secrets Store CSI Driver, and this provider's DaemonSet.
#
# It is idempotent: re-running reuses a running cluster and upgrades in place.
# The e2e Go test does NOT install this infrastructure; it assumes setup.sh has
# already run. See test/README.md for the full runbook.
#
# Requirements on PATH: k3s, ko, helm, kubectl (k3s provides kubectl/ctr).
# Run as root (k3s and the provider DaemonSet require it).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

IMAGE_REPO="${E2E_IMAGE_REPO:-safeguard-csi-provider}"
IMAGE_TAG="${E2E_IMAGE_TAG:-e2e}"
K3S_LOG="${K3S_LOG:-/var/log/k3s.log}"

log() { printf '\n=== %s ===\n' "$*"; }

# Under WSL "mirrored" networking the lab/host-only interface that reaches the
# appliance is mirrored WITHOUT a default route. k3s needs a default route to
# pick a node IP, and pods need one for egress (to the appliance and registries).
# Derive the primary global-scope interface/address and, when no default route
# exists, add one via that link's gateway. Override any of these with E2E_* to
# skip auto-detection on differently-shaped networks.
NODE_IP="${E2E_NODE_IP:-}"
FLANNEL_IFACE="${E2E_FLANNEL_IFACE:-}"
if [[ -z "$NODE_IP" || -z "$FLANNEL_IFACE" ]]; then
  read -r _ifc _ip < <(ip -o -4 addr show scope global | awk '{split($4,a,"/"); print $2, a[1]; exit}')
  NODE_IP="${NODE_IP:-$_ip}"
  FLANNEL_IFACE="${FLANNEL_IFACE:-$_ifc}"
fi
if [[ -n "$FLANNEL_IFACE" ]] && ! ip route show default | grep -q .; then
  GW="${E2E_DEFAULT_GATEWAY:-$(ip route show | awk '/ via / {print $3; exit}')}"
  if [[ -n "$GW" ]]; then
    log "No default route; adding default via $GW dev $FLANNEL_IFACE (WSL mirrored mode)"
    ip route add default via "$GW" dev "$FLANNEL_IFACE" || true
  fi
fi

K3S_ARGS=(--write-kubeconfig-mode 644 --disable traefik --disable metrics-server)
[[ -n "$NODE_IP" ]] && K3S_ARGS+=(--node-ip "$NODE_IP")
[[ -n "$FLANNEL_IFACE" ]] && K3S_ARGS+=(--flannel-iface "$FLANNEL_IFACE")

log "Ensuring k3s server is running"
if ! k3s kubectl get nodes >/dev/null 2>&1; then
  nohup k3s server "${K3S_ARGS[@]}" >"$K3S_LOG" 2>&1 &
  for _ in $(seq 1 60); do
    if k3s kubectl get nodes 2>/dev/null | grep -q ' Ready '; then break; fi
    sleep 3
  done
fi
k3s kubectl get nodes -o wide

log "Building provider image with ko and importing into k3s"
( cd "$REPO_ROOT"
  export KO_DOCKER_REPO="$IMAGE_REPO"
  export GOFLAGS=-mod=mod
  ko build ./cmd --bare --tags "$IMAGE_TAG" --platform=linux/amd64 \
    --push=false --tarball=/tmp/provider-e2e.tar
)
k3s ctr images import /tmp/provider-e2e.tar

log "Installing the Secrets Store CSI Driver"
helm repo add secrets-store-csi-driver \
  https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts >/dev/null 2>&1 || true
helm repo update >/dev/null 2>&1
helm upgrade --install csi-secrets-store \
  secrets-store-csi-driver/secrets-store-csi-driver \
  --namespace kube-system \
  --set linux.kubeletRootDir=/var/lib/kubelet \
  --set linux.providersDir=/etc/kubernetes/secrets-store-csi-providers \
  --set syncSecret.enabled=false \
  --set enableSecretRotation=false \
  --wait --timeout 180s

log "Installing the Safeguard CSI provider"
helm upgrade --install safeguard-csi-provider \
  "$REPO_ROOT/charts/safeguard-csi-provider" \
  --namespace kube-system \
  --set "linux.image.repository=docker.io/library/${IMAGE_REPO}" \
  --set "linux.image.tag=${IMAGE_TAG}" \
  --set linux.image.pullPolicy=Never \
  --wait --timeout 120s

log "Ready"
kubectl -n kube-system get pods -o wide | grep -Ei 'secrets-store|safeguard'
