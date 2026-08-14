# Contributing to safeguard-csi-provider

Thanks for your interest in improving safeguard-csi-provider, the
Kubernetes [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)
provider for One Identity Safeguard for Privileged Passwords.

## Reporting issues

- **Bugs and feature requests:** open a GitHub Issue.
- **Security vulnerabilities:** do **not** open a public issue — follow
  [SECURITY.md](SECURITY.md).

## Prerequisites

- [Go 1.25](https://go.dev/dl/) or later.
- [Docker](https://docs.docker.com/get-docker/) (with Buildx) to build
  container images.
- For the end-to-end tests: [kind](https://kind.sigs.k8s.io/) and
  [kubectl](https://kubernetes.io/docs/tasks/tools/).
- (Optional) access to a Safeguard for Privileged Passwords appliance.

This repository uses a git submodule for the documentation theme. If you
build the docs site, initialize it first:

    git submodule update --init --recursive

## Building

Build the provider binary (defaults to a Linux `amd64` build):

    make build

Other targets are available for cross-compilation and container images:

    make build-windows
    make build-darwin
    make container         # local Docker image
    make container-linux   # buildx multi-arch image

## Testing

Unit tests run with the race detector and coverage and exclude the
end-to-end suite:

    make unit-test

The end-to-end tests under `test/e2e` exercise the provider inside a
[kind](https://kind.sigs.k8s.io/) cluster and require Docker, kind, and
kubectl.

## Linting and dependencies

    make lint   # golangci-lint + misspell
    make mod    # go mod tidy

## A note on TLS

The provider exposes an `insecureSkipVerify` configuration parameter that
bypasses appliance certificate verification. It is for bootstrapping and
development only against appliances without a CA-signed certificate — do
not enable it in production.

## Submitting changes

1. Fork the repository and create a feature branch.
2. Keep commits focused with clear messages.
3. Ensure `make build`, `make lint`, and `make unit-test` pass.
4. Open a pull request describing the behavior you changed and the tests
   that prove it.
