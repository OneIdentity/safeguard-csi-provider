# Configuration reference

Secrets are requested through a `SecretProviderClass` with `provider: safeguard`.
The client certificate used to authenticate is supplied separately through the
CSI driver's `nodePublishSecretRef`.

## Authentication model

The provider authenticates to Safeguard's Application-to-Application (A2A)
service with a **client certificate** over mutual TLS. There is no username,
password, or long-lived bearer token stored in the cluster.

On each mount the provider:

1. Builds an A2A context from the client certificate and key.
2. Lists every account the certificate's A2A registrations expose
   (`GetRetrievableAccounts`), each carrying a per-account API key.
3. Filters those accounts to the requested `appName` (the registration's
   application name), or keeps all of them when `appName` is empty.
4. Retrieves each account's credential in the requested `objectType`.

## SecretProviderClass parameters

```yaml
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: safeguard
spec:
  provider: safeguard
  parameters:
    safeguardHost: safeguard.example.com
    appName: my-application
    objectType: Password
```

| Parameter | Required | Default | Description |
| --- | --- | --- | --- |
| `safeguardHost` | yes | — | Hostname of the Safeguard appliance, e.g. `safeguard.example.com`. Do not include a scheme or path. |
| `appName` | no | *(all)* | A2A registration application name. Only accounts belonging to this registration are mounted. Empty mounts every account the certificate can retrieve. |
| `objectType` | no | `Password` | Credential type to retrieve for all matched accounts: `Password`, `PrivateKey`, or `ApiKey` (case-insensitive). |
| `keyFormat` | no | `OpenSsh` | SSH private-key format, used only when `objectType: PrivateKey`: `OpenSsh`, `Ssh2`, or `Putty`. |
| `safeguardCaBundle` | no | *(system roots)* | PEM CA bundle used to verify the appliance certificate. Set this when the appliance uses a privately issued certificate. |
| `insecureSkipVerify` | no | `false` | When `"true"`, disables appliance certificate verification. **Bootstrapping only — never use in production.** |

## Node-publish secret

The client certificate and key are provided through a Kubernetes `Secret`
referenced by the volume's `nodePublishSecretRef`:

| Key | Description |
| --- | --- |
| `clientCertificate` | PEM-encoded client certificate (leaf plus any intermediates). |
| `clientKey` | PEM-encoded private key for the client certificate. |

Only PEM material is accepted. Convert PKCS#12 first:

```bash
openssl pkcs12 -in client.pfx -clcerts -nokeys  -out clientCertificate.pem
openssl pkcs12 -in client.pfx -nocerts  -nodes  -out clientKey.pem
```

Create and label the secret:

```bash
kubectl create secret generic safeguard-a2a \
  --from-file=clientCertificate=clientCertificate.pem \
  --from-file=clientKey=clientKey.pem
kubectl label secret safeguard-a2a secrets-store.csi.k8s.io/used=true
```

## Account-to-file mapping

Each retrieved account produces **one file** in the mount, named after the
account (`AccountName`). The file contents depend on `objectType`:

| `objectType` | File contents |
| --- | --- |
| `Password` | Plaintext password. |
| `PrivateKey` | SSH private key in the requested `keyFormat`. |
| `ApiKey` | JSON array of the account's API key credentials: `id`, `name`, `description`, `clientId`, `clientSecret`, `clientSecretId`. |

If two registrations expose accounts with the same name and `appName` is empty,
they map to the same file name; set `appName` to disambiguate.

## Appliance certificate trust

By default the provider verifies the appliance certificate against the node's
system root CAs. Most Safeguard appliances present a privately issued
certificate, so supply the issuing CA chain with `safeguardCaBundle`:

```yaml
parameters:
  safeguardCaBundle: |
    -----BEGIN CERTIFICATE-----
    ...appliance issuing CA...
    -----END CERTIFICATE-----
```

Use `insecureSkipVerify: "true"` only to bootstrap against a self-signed
appliance; it disables verification entirely and is unsafe for production.

## Versioning and rotation caveat

The provider reports a **new object version on every mount**, so when the driver
is run with `--enable-secret-rotation` it treats the credential as changed on
every rotation poll and rewrites the mount (and any synced Kubernetes Secret).
This means new credential values are always picked up, but writes also happen
when nothing changed. See [rotation behavior](./troubleshooting.md#rotation-behavior).
