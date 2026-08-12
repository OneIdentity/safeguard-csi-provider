# Live and end-to-end test harness

These suites exercise the provider against a **real Safeguard appliance** and,
for the end-to-end layer, a **real Kubernetes (k3s) node**. They are deliberately
kept out of `go test ./...` and CI by build tags, because they require secrets,
network access, and a mutable throwaway appliance.

> **Use a throwaway/test appliance only.** The harness creates and deletes users,
> assets, accounts, trusted certificates, and A2A registrations. Every object is
> named with a unique run ID and cleaned up in reverse order, but you should
> still point this at an appliance you are comfortable mutating.

## Layers

| Layer | Build tag | What it proves | Location |
| --- | --- | --- | --- |
| 0 — Live A2A | `live` | The SDK + a generated client certificate complete real password / SSH key / API key retrievals against the appliance. | `test/live` |
| 2 — End-to-end | `e2e` | The CSI driver → provider DaemonSet → pod mount path writes the real credential into a pod on a real kubelet. | `test/e2e` |

Layer 1 (provider gRPC over a socket, no cluster) is intentionally skipped: the
end-to-end layer covers the same path with more fidelity.

The shared provisioning, PKI, and cleanup code lives in `test/harness` and has no
build tag, so it compiles with the normal build but never runs on its own.

## What a run does

1. Connect with the bootstrap administrator credential.
2. Create a dedicated, run-scoped test administrator.
3. Provision a dummy asset (the connectivity-free "Other Managed" platform), an
   account with a known password, an SSH key, and an API key credential.
4. Generate a CA + client certificate, upload the CA as trusted, and create a
   certificate user mapped to the client-certificate thumbprint.
5. Create an A2A registration exposing the account's password, SSH key, and API
   key to that certificate user.
6. Retrieve each credential and assert it matches (Layer 0), or mount it into a
   pod and assert the file contents (Layer 2).
7. Delete everything created, in reverse order (unless `SAFEGUARD_KEEP=1`).

## Configuration (environment variables)

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `SAFEGUARD_HOST` | yes | — | Appliance hostname (no scheme/path). |
| `SAFEGUARD_BOOTSTRAP_PASSWORD` | yes | — | Bootstrap administrator password. |
| `SAFEGUARD_BOOTSTRAP_USER` | no | `admin` | Bootstrap administrator username. |
| `SAFEGUARD_BOOTSTRAP_PROVIDER` | no | `local` | Authentication provider for the bootstrap login. |
| `SAFEGUARD_CA_BUNDLE_FILE` | no | *(system roots)* | Path to a PEM CA bundle that verifies the appliance certificate. |
| `SAFEGUARD_INSECURE` | no | `false` | Skip appliance certificate verification (throwaway appliances only). |
| `SAFEGUARD_KEEP` | no | `false` | Leave provisioned objects in place for debugging. |
| `SAFEGUARD_RUN_ID` | no | `csi-e2e-<unix>` | Override the unique per-run object-name prefix. |

## Running Layer 0 (live A2A)

```bash
export SAFEGUARD_HOST=sg.test.example.com
export SAFEGUARD_BOOTSTRAP_PASSWORD='...'
export SAFEGUARD_INSECURE=true          # or SAFEGUARD_CA_BUNDLE_FILE=/path/ca.pem

go test -tags live -v ./test/live/...
```

## Running Layer 2 (end-to-end on k3s)

Layer 2 must run **on a Linux host** because it drives `k3s`, `kubectl`, and
`helm` locally. On Windows, run it inside **WSL2**.

Prerequisites on the Linux host / WSL2:

- `k3s` (the harness starts `k3s server` directly; WSL2 needs no systemd)
- `kubectl` and `helm` on `PATH`
- A container builder (`docker` or `nerdctl`) to build and import the provider
  image into k3s via `k3s ctr images import`

```bash
# same SAFEGUARD_* exports as Layer 0, then:
go test -tags e2e -v ./test/e2e/...
```

## Cleanup and debugging

- Normal runs delete everything they create.
- `SAFEGUARD_KEEP=1` leaves objects in place; the run logs their names so you can
  inspect or delete them by hand.
- Re-running is safe: each run uses a fresh `SAFEGUARD_RUN_ID`, so runs never
  collide, and cleanup treats a missing object (404) as already deleted.
