# Usage

This guide shows how to mount Safeguard credentials into a pod, sync them to a
native Kubernetes `Secret`, and retrieve different credential types.

## Mount credentials into a pod

### 1. Define a SecretProviderClass

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
    safeguardCaBundle: |
      -----BEGIN CERTIFICATE-----
      ...appliance issuing CA...
      -----END CERTIFICATE-----
```

See the [configuration reference](./configuration.md) for every parameter.

### 2. Mount it in a pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: app
spec:
  containers:
    - name: app
      image: registry.example.com/app:latest
      volumeMounts:
        - name: safeguard
          mountPath: /mnt/secrets
          readOnly: true
  volumes:
    - name: safeguard
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes:
          secretProviderClass: safeguard
        nodePublishSecretRef:
          name: safeguard-a2a
```

Each retrievable account appears as a file under `/mnt/secrets`, named after the
account. Read a password with:

```bash
cat /mnt/secrets/my-service-account
```

Ready-to-edit manifests live in [`examples/`](../examples).

## Sync to a Kubernetes Secret

To expose credentials as environment variables you must sync the mounted files
to a native `Secret` using `secretObjects`. This requires the driver to be
installed with `syncSecret.enabled=true`.

```yaml
spec:
  provider: safeguard
  secretObjects:
    - secretName: app-credentials
      type: Opaque
      data:
        - objectName: my-service-account   # the Safeguard account name
          key: password
  parameters:
    safeguardHost: safeguard.example.com
    appName: my-application
    objectType: Password
```

`objectName` is the mounted file name — i.e. the Safeguard **account name**.
The synced `Secret` is created when a pod that mounts the volume starts and is
removed when the last such pod is deleted.

Consume it as environment variables:

```yaml
    env:
      - name: DB_PASSWORD
        valueFrom:
          secretKeyRef:
            name: app-credentials
            key: password
```

> The pod must still mount the CSI volume; `secretObjects` only mirrors what the
> mount produced.

## Retrieve other credential types

### SSH private keys

```yaml
  parameters:
    objectType: PrivateKey
    keyFormat: OpenSsh      # or Ssh2, Putty
```

Each account's file contains its SSH private key in the requested format. Key
material is normalized to LF line endings so Linux pods can consume it directly.

### API keys

```yaml
  parameters:
    objectType: ApiKey
```

Each account's file contains a JSON array of that account's API key
credentials, each with `id`, `name`, `description`, `clientId`, `clientSecret`,
and `clientSecretId`.

## Retrieving multiple accounts

A single `SecretProviderClass` retrieves **every** account exposed by the
matched A2A registration(s), producing one file per account. Leave `appName`
empty to retrieve every account the client certificate can access across all its
registrations. Set `accountNames` (comma-separated, case-insensitive) to narrow
the mount to specific accounts. In file-per-account mode all accounts share the
same `objectType`; use separate `SecretProviderClass` objects, or bundle mode,
when you need different types.

## Serving different secrets to different pods

Because each mount is scoped by its `SecretProviderClass` (`appName` /
`accountNames`) **and** by the client certificate in its `nodePublishSecretRef`,
different pods can receive entirely different credentials from the same provider.
The client certificate is the real authorization boundary: a pod can only ever
receive accounts its A2A registration is entitled to retrieve.

## Bundling many credentials into one file

For higher density, `outputFormat: bundle` writes a single JSON file keyed by
account name, carrying every credential type in `objectTypes`:

```yaml
  parameters:
    appName: my-application
    accountNames: db-admin,svc-account
    outputFormat: bundle
    objectTypes: Password,PrivateKey,ApiKey
    keyFormat: OpenSsh
```

This produces one `secrets.json` (one file, one inotify watch) instead of many
files, while still keeping everything in tmpfs and nothing in etcd. Each account
carries only the credential types it actually has; a missing type is omitted and
the mount still succeeds. For example, a `db-admin` with all three types
alongside a password-only `svc-account` renders as:

```json
{
  "db-admin": {
    "password": "S0me-R0tated-P@ssw0rd",
    "privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----\n",
    "apiKey": [ { "id": 42, "name": "db-admin-api", "clientId": "...", "clientSecret": "...", "clientSecretId": "..." } ]
  },
  "svc-account": {
    "password": "an0ther-r0tated-secret"
  }
}
```

See the
[configuration reference](./configuration.md#bundle-outputformat-bundle) for the
full JSON shape and field list.

## Next steps

- [Troubleshooting](./troubleshooting.md)
- [Configuration reference](./configuration.md)
