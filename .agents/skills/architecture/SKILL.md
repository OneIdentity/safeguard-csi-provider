---
name: architecture
description: Use when reasoning about how the provider is wired — cmd/main gRPC server startup, the CSIDriverProviderServer (Mount/Version) and health service, the healthz probe, the mount request contract, and how the provider drives the safeguard-go A2A SDK to retrieve and shape credentials.
---

# Architecture

`safeguard-csi-provider` is a gRPC server implementing the Secrets Store CSI
Driver provider contract. The in-cluster driver DaemonSet calls it over a Unix
socket; it authenticates to Safeguard's A2A service with the pod's client
certificate and returns credential file contents to mount.

## Startup (`cmd/main.go`)

1. `klog.InitFlags`; optional `--log-format-json` swaps in a `zapr`+`zap` JSON
   logger via `klog.SetLogger` (this is the seam that broke when
   `k8s.io/component-base/logs/json.JSONLogger` was removed upstream — it is now
   a direct zapr logger, not a component-base dependency).
2. Parse `--endpoint` (default `unix:///tmp/safeguard.sock`), remove any stale
   socket, `net.Listen`.
3. Build a `grpc.NewServer` with the `utils.LogInterceptor()` unary interceptor.
4. Register two services on the one server:
   - `k8spb.RegisterCSIDriverProviderServer` → `server.New()` (Mount, Version)
   - `grpc_health_v1.RegisterHealthServer` → the same object (Check, Watch, List)
5. Start an HTTP `healthz` probe (`server.HealthZ`) that dials the provider's own
   Unix socket and issues a gRPC health `Check`.
6. Block on SIGTERM/SIGINT, then `GracefulStop`.

## The server (`pkg/server`)

`CSIDriverProviderServer` embeds `*grpc.Server` and
`grpc_health_v1.UnimplementedHealthServer` (forward-compat: the health interface
gained `List`; embedding provides the default while explicit `Check`/`Watch`
override it). It implements:

- **`Mount`** — unmarshals `attributes`, `secrets`, and `permission` from the
  request, delegates to `provider.MountSecretsStoreObjectContent`, and maps the
  returned `files` + `objectVersions` into a `v1alpha1.MountResponse`. Files in
  the response are what the driver writes to the pod (no files ⇒ "not implemented"
  to the driver).
- **`Version`** — returns `v1alpha1`, the build-stamped runtime version, and the
  runtime name.
- **health `Check`** — always `SERVING`; `Watch` returns `Unimplemented`.

`pkg/server/healthz.go` is the liveness/readiness HTTP endpoint. It dials the
provider's Unix socket with `grpc.NewClient("passthrough:///"+path, ...)` plus a
custom context dialer (the `passthrough:///` scheme hands the raw socket path to
the dialer; `grpc.Dial`/`WithInsecure` were removed because they are
staticcheck-deprecated). Credentials are `insecure.NewCredentials()` — the socket
is local and unauthenticated by design.

## The mount logic (`pkg/provider/provider.go`)

`MountSecretsStoreObjectContent` is the heart of the provider:

1. Read `safeguardHost`, `appName`, `accountNames`, output config, and appliance
   trust from `attributes`; read `clientCertificate`/`clientKey` from `secrets`.
2. Build a `safeguard.A2AContext` (SDK) from the client cert + key, applying
   `WithCABundle` (`safeguardCaBundle`) or `WithInsecureTLS`
   (`insecureSkipVerify`, bootstrap only, logged loudly).
3. `a2a.GetRetrievableAccounts(ctx, "")` — every account this certificate can
   retrieve, across all A2A registrations, each carrying its own per-account API
   key. Filter by `appName` (registration) and `accountNames` (case-insensitive).
4. For each matched account, retrieve the requested object type(s):
   - `Password` → `a2a.RetrievePassword`
   - `PrivateKey` → `a2a.RetrievePrivateKey` (+ `keyFormat`), normalized to LF
   - `ApiKey` → `a2a.RetrieveAPIKey`, serialized to JSON with the exposed secret
5. Shape output:
   - **file-per-account** (default): one file per account, named after the
     account, carrying one object type.
   - **bundle**: a single JSON file (`bundleFile`, default `secrets.json`) keyed
     by account, carrying every requested type the account actually has. Absent
     types (empty password/key, or `[]` API keys) are omitted, not errored.

Design intent baked into the code: heterogeneous accounts are normal, so a
"credential type this account lacks" is an expected, logged-at-info miss — only
genuine retrieval failures (auth/network/appliance) are warnings, so a normal
setup does not trip error-based alerting.

## Why the shape

- **`v1alpha1` is the stable seam.** The provider only depends on the generated
  `provider/v1alpha1` gRPC types, which are unchanged across CSI-driver releases,
  so the imported `secrets-store-csi-driver` module version and the in-cluster
  driver version are independent.
- **safeguard-go owns all Safeguard I/O.** This repo never speaks the Safeguard
  Web API directly; it only orchestrates the SDK's `A2AContext`. Auth, TLS trust,
  token/credential handling, and the `Secret` redaction type all live in the SDK.
- **Secrets stay in `Secret` until the write boundary.** Plaintext is produced
  only via `Expose`/`ExposeString` at the moment bytes are handed to the mount.
