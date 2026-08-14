---
name: testing-guide
description: Use when writing or running tests — the default unit tests, the gated live A2A suite (-tags live), or the e2e suite (-tags e2e). Covers the SAFEGUARD_* env vars, the throwaway-appliance requirement, and the WSL2/k3s runbook. The authoritative runbook is test/README.md.
---

# Testing Guide

Three layers. Unit tests run everywhere; the two tagged suites prove real
behavior against a real appliance (and, for e2e, a real Kubernetes node). The
full, canonical runbook — every env var, the provisioning steps, and WSL2
networking — is [`test/README.md`](../../../test/README.md); this skill is the
orientation and the gotchas.

## Unit tests (default, hermetic)

```sh
go test ./...            # live/e2e are behind build tags, so they don't run here
make unit-test           # CGO race build (needs gcc/clang) + coverage
```

`pkg/provider` carries the meaningful unit coverage (output-config parsing,
object-type normalization, bundle assembly). Keep these fast and fake-based.

## Gated suites — throwaway appliance only

> Both suites **mutate the appliance**: they create and delete a run-scoped test
> admin, an "Other Managed" asset, an account with a password/SSH key/API key, a
> trusted CA + certificate user, and an A2A registration, then clean up in
> reverse order (unless `SAFEGUARD_KEEP=1`). Point them at a lab appliance you are
> comfortable mutating — never production.

Shared provisioning/PKI/cleanup lives in `test/harness` (no build tag, so it
always compiles but never runs alone).

### Environment variables

| Variable | Required | Default | Notes |
|---|---|---|---|
| `SAFEGUARD_HOST` | yes | — | Appliance hostname, no scheme/path |
| `SAFEGUARD_BOOTSTRAP_PASSWORD` | yes | — | Bootstrap admin password (secret) |
| `SAFEGUARD_BOOTSTRAP_USER` | no | `admin` | Bootstrap admin username |
| `SAFEGUARD_BOOTSTRAP_PROVIDER` | no | `local` | Auth provider for bootstrap login |
| `SAFEGUARD_CA_BUNDLE_FILE` | no | system roots | PEM bundle trusting the appliance cert |
| `SAFEGUARD_INSECURE` | no | `false` | Skip appliance cert verification (lab only) |
| `SAFEGUARD_KEEP` | no | `false` | Leave provisioned objects for debugging |
| `SAFEGUARD_RUN_ID` | no | `csi-e2e-<unix>` | Per-run object-name prefix |

### Live A2A suite (`-tags live`) — light smoke test

Needs only a reachable appliance; runs on Windows via Go.

```sh
export SAFEGUARD_HOST=sg.lab.example.com
export SAFEGUARD_BOOTSTRAP_PASSWORD='...'
export SAFEGUARD_INSECURE=true          # or SAFEGUARD_CA_BUNDLE_FILE=/path/ca.pem
go test -tags live -v ./test/live/...
```

Proves the SDK + a generated client certificate complete real password / SSH key
/ API key retrievals. This is the fast confidence check to run after touching
retrieval logic or bumping the SDK / k8s stack.

### End-to-end suite (`-tags e2e`) — heavy

Must run on Linux (or **WSL2** on Windows) as **root**, driving `k3s`, `kubectl`,
`helm`, and [`ko`](https://ko.build).

```sh
sudo test/e2e/setup.sh                   # once; idempotent — brings up k3s + driver + provider DaemonSet
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
# same SAFEGUARD_* exports as the live suite, then:
sudo -E env "PATH=$PATH" go test -tags e2e -v -timeout 900s ./test/e2e/...
```

`setup.sh` owns the infrastructure and image build/import; the Go test only
creates per-run namespace/`SecretProviderClass`/pod and tears them down.
`TestE2EMount` asserts **FilePerAccount** (password byte-exact, SSH key LF-clean
and parseable, API-key JSON carries the client secret) and **Bundle** (one
`secrets.json` with all three types).

After changing provider code, rebuild+reimport the image and force a rollout so
the DaemonSet picks it up (`setup.sh` handles the rebuild; then
`kubectl -n kube-system rollout restart daemonset/safeguard-csi-provider`).

### WSL2 networking gotcha

If the appliance is on a host-only/lab network WSL2's NAT can't route to, set
`networkingMode=mirrored` in `%USERPROFILE%\.wslconfig` and `wsl --shutdown`.
`setup.sh` compensates for mirrored-mode's missing default route; override with
`E2E_NODE_IP` / `E2E_FLANNEL_IFACE` / `E2E_DEFAULT_GATEWAY`.

## When to run what

- Provider/output-shaping logic change → unit tests + **live**.
- SDK bump, or the k8s/gRPC/klog dependency stack → **live** and, because the
  driver generation may have moved, **e2e**.
- Release candidate → **live** and **e2e** green against a lab appliance before
  tagging. CI cannot run either (no appliance is reachable), so this is on the
  developer/agent, not the pipeline.
