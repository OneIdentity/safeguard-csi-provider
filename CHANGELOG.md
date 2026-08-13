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
- `accountNames` parameter: a multivalued, case-insensitive account-name filter
  applied in addition to `appName`, so one provider can serve different accounts
  to different pods based on their `SecretProviderClass`.
- `outputFormat: bundle` mode that writes a single JSON file (`bundleFile`,
  default `secrets.json`) keyed by account name, carrying every `objectTypes`
  value per account for higher secret density, still tmpfs-only with nothing in
  etcd. `objectTypes` selects the credential types included per account.
- `keyFormat` parameter for `PrivateKey` retrieval: `OpenSsh` (default), `Ssh2`,
  or `Putty`.
- Live end-to-end (k3s) test proving password, SSH private key, and API key all
  mount and are pod-interpretable in both file-per-account and bundle modes.
- `safeguardCaBundle` parameter to trust a privately issued appliance
  certificate.
- `insecureSkipVerify` parameter to bypass appliance certificate verification
  for bootstrapping (not for production).
- Hermetic unit tests using an in-process TLS fake appliance.
- Documentation set under `docs/` and expanded examples.
- Apache 2.0 `LICENSE` and `NOTICE`.

### Changed

- Appliance certificate trust now uses `safeguardCaBundle` (or system roots)
  instead of relying solely on system roots with no override.
- When `appName` is set but matches no retrievable accounts, the provider now
  returns a clear error; an empty `appName` retrieves all accessible accounts.
  The same clear error is returned when `accountNames` matches nothing.
- Private-key material is normalized to LF line endings before mounting.
  Safeguard returns key material with Windows CRLF, which a lone trailing CR can
  make unparseable to strict PEM/SSH parsers; passwords are unchanged.

### Removed

- Bespoke request/response models and helper utilities now provided by the SDK.

### Known limitations

- The reported object version changes on every mount, so
  `--enable-secret-rotation` rewrites mounts on every poll regardless of whether
  the credential changed. See
  [rotation behavior](./docs/troubleshooting.md#rotation-behavior).
