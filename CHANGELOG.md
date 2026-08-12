# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Adopted the [`safeguard-go`](https://github.com/OneIdentity/safeguard-go) SDK
  (v0.9.0) for all Safeguard A2A communication, replacing the hand-rolled
  HTTP/RSTS/A2A client.
- `objectType` parameter to select the retrieved credential type: `Password`
  (default), `PrivateKey`, or `ApiKey`.
- `keyFormat` parameter for `PrivateKey` retrieval: `OpenSsh` (default), `Ssh2`,
  or `Putty`.
- `safeguardCaBundle` parameter to trust a privately issued appliance
  certificate.
- `insecureSkipVerify` parameter to bypass appliance certificate verification
  for bootstrapping (not for production).
- Hermetic unit tests using an in-process TLS fake appliance.
- GitHub Actions CI (tidy check, vet, race tests, Linux/Windows build).
- Documentation set under `docs/` and expanded examples.
- Apache 2.0 `LICENSE` and `NOTICE`.

### Changed

- Appliance certificate trust now uses `safeguardCaBundle` (or system roots)
  instead of relying solely on system roots with no override.
- When `appName` is set but matches no retrievable accounts, the provider now
  returns a clear error; an empty `appName` retrieves all accessible accounts.

### Removed

- Bespoke request/response models and helper utilities now provided by the SDK.

### Known limitations

- The reported object version changes on every mount, so
  `--enable-secret-rotation` rewrites mounts on every poll regardless of whether
  the credential changed. See
  [rotation behavior](./docs/troubleshooting.md#rotation-behavior).
