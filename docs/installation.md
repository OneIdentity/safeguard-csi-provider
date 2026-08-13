# Installation

The Safeguard CSI Provider runs as a `DaemonSet` on each node and serves the
Secrets Store CSI Driver over a Unix socket. The driver must be installed
separately.

## Prerequisites

- A Kubernetes cluster (Linux and/or Windows nodes).
- The [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)
  installed in the cluster.
- Network reachability from cluster nodes to the Safeguard appliance over HTTPS
  (443).
- A configured Safeguard A2A registration and client certificate — see the
  [Safeguard A2A setup guide](./safeguard-setup.md).

## Compatibility

| Component | Version |
| --- | --- |
| Go (build) | 1.21+ |
| Secrets Store CSI Driver | 0.3.0+ |
| CSI provider protocol | `v1alpha1` |
| safeguard-go SDK | v0.9.0 |

## 1. Install the Secrets Store CSI Driver

If it is not already present, install the driver (Helm shown):

```bash
helm repo add secrets-store-csi-driver https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts
helm install csi-secrets-store secrets-store-csi-driver/secrets-store-csi-driver \
  --namespace kube-system
```

To use [secret sync](./usage.md#sync-to-a-kubernetes-secret) or
[auto rotation](./troubleshooting.md#rotation-behavior), enable the
corresponding driver features:

```bash
--set syncSecret.enabled=true \
--set enableSecretRotation=true
```

## 2. Install the Safeguard CSI Provider

### Helm (recommended)

Install the packaged chart attached to a
[GitHub Release](https://github.com/OneIdentity/safeguard-csi-provider/releases)
(replace `X.Y.Z`), which pins the matching provider image by default:

```bash
helm install safeguard-csi-provider \
  https://github.com/OneIdentity/safeguard-csi-provider/releases/download/vX.Y.Z/safeguard-csi-provider-X.Y.Z.tgz
```

Or install from a checkout of the source tree (uses the chart's `appVersion` as
the image tag):

```bash
helm install safeguard-csi-provider ./charts/safeguard-csi-provider
```

Key `values.yaml` settings:

| Value | Default | Description |
| --- | --- | --- |
| `linux.image.repository` | `ghcr.io/oneidentity/safeguard-csi-provider` | Provider image. |
| `linux.image.tag` | chart `appVersion` | Image tag; empty inherits the chart version. Set only to override. |
| `linux.enabled` | `true` | Deploy the Linux DaemonSet. |
| `windows.enabled` | `false` | Deploy the Windows DaemonSet. |
| `logFormatJSON` | `false` | Emit JSON logs. |
| `logVerbosity` | `0` | klog verbosity (`-v`). |
| `linux.healthzPort` | `8989` | Liveness probe port. |

### Raw manifests

Apply the rendered install manifest attached to a release (replace `X.Y.Z`):

```bash
kubectl apply -f https://github.com/OneIdentity/safeguard-csi-provider/releases/download/vX.Y.Z/safeguard-csi-provider-X.Y.Z.yaml
```

The legacy standalone manifests in [`deployment/`](../deployment) are also kept
in sync with the chart version:

```bash
kubectl apply -f deployment/provider-azure-installer.yaml
# Windows nodes:
kubectl apply -f deployment/provider-azure-installer-windows.yaml
```

The provider registers itself with the driver under the name **`safeguard`**
(the socket filename), which is the value you set in a `SecretProviderClass`'s
`provider` field.

## 3. Verify

```bash
kubectl get pods -l app=safeguard-csi-provider
kubectl logs -l app=safeguard-csi-provider
```

The provider serves a liveness probe on `--healthz-port` (default `8989`) at
`--healthz-path` (default `/healthz`).

## Runtime flags

The provider binary accepts:

| Flag | Default | Description |
| --- | --- | --- |
| `--endpoint` | `unix:///tmp/safeguard.sock` | CSI gRPC endpoint. |
| `--healthz-port` | `8989` | Health-check port. |
| `--healthz-path` | `/healthz` | Health-check path. |
| `--healthz-timeout` | `5s` | Health-check RPC timeout. |
| `--log-format-json` | `false` | JSON log formatter. |
| `-v` | `0` | klog verbosity level. |
| `--version` | | Print version and exit. |

## Next steps

- [Configure a SecretProviderClass](./configuration.md)
- [Mount secrets into a pod](./usage.md)
