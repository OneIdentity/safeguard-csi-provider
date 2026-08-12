# Troubleshooting

## Where to look

The provider runs as a DaemonSet; the driver runs its own pods. Check both:

```bash
# provider logs (credential retrieval happens here)
kubectl logs -l app=safeguard-csi-provider

# driver logs (mount orchestration, secret sync, rotation)
kubectl logs -l app=secrets-store-csi-driver -n kube-system
```

Mount failures surface on the consuming pod:

```bash
kubectl describe pod <pod>
```

## Logging and verbosity

The provider uses klog. Increase detail with `-v` and switch to structured
output with `--log-format-json` (or the chart's `logVerbosity` / `logFormatJSON`
values):

```yaml
# charts/safeguard-csi-provider/values.yaml
logFormatJSON: true
logVerbosity: 4
```

## Common errors

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `requested app name <x> had no retrievable accounts` | `appName` doesn't match any A2A registration, or the registration exposes no accounts. | Confirm the registration name and that accounts are assigned to it. |
| Warning that zero accounts were retrieved | The certificate has no A2A registrations, or none expose accounts. | Verify the certificate user and its Retrievable Accounts in SPP. |
| TLS / `x509: certificate signed by unknown authority` | The appliance's issuing CA isn't trusted. | Provide `safeguardCaBundle` with the appliance issuing CA. |
| TLS handshake / client cert rejected | Wrong client certificate, or its thumbprint isn't mapped to the SPP certificate user. | Re-check steps 1–2 of the [Safeguard setup](./safeguard-setup.md). |
| `invalid objectType` | `objectType` isn't `Password`, `PrivateKey`, or `ApiKey`. | Correct the parameter (case-insensitive). |
| Retrieval succeeds for some accounts, not others | An individual account isn't retrievable by this registration. | Failing accounts are logged and skipped; assign them in SPP. |
| Connection refused / timeout to appliance | Network path or IP restriction. | Confirm node → appliance reachability and any A2A IP restrictions (nodes use `hostNetwork`). |

## Certificate material

- Only **PEM** is accepted for `clientCertificate` / `clientKey`. Convert PKCS#12
  with `openssl pkcs12` (see [configuration](./configuration.md#node-publish-secret)).
- The `nodePublishSecretRef` secret must live in the **same namespace** as the
  consuming pod and be labeled `secrets-store.csi.k8s.io/used=true`.

## Health checks

The provider serves a liveness endpoint on `--healthz-port` (default `8989`) at
`--healthz-path` (default `/healthz`). If liveness fails, the pod restarts before
it can serve mounts; check the provider logs for a startup error.

## Rotation behavior

The provider reports a **new object version on every mount**. When the driver
runs with `--enable-secret-rotation` and a `--rotation-poll-interval`, it compares
object versions to decide whether to rewrite a mount. Because this provider's
version always changes, the driver rewrites the mount (and any synced Secret) on
**every poll**, whether or not the underlying credential changed.

Implications:

- New credential values are always picked up within one poll interval.
- Writes also occur when nothing changed, adding avoidable churn. Choose a
  `--rotation-poll-interval` that balances freshness against load.

This stems from a known limitation (per-account versions are not yet derived from
Safeguard). It is safe but not optimal; a future release intends to report a
stable, content-derived version.

## Still stuck?

Raise verbosity to `-v 4`, reproduce, and open an issue with the provider and
driver logs (redact secret values).
