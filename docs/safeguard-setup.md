# Safeguard A2A setup

Before the provider can retrieve credentials, Safeguard for Privileged Passwords
(SPP) must be configured with an Application-to-Application (A2A) registration
tied to the client certificate the provider will present. These steps are
performed by a Safeguard administrator in the SPP web console.

> The exact menu labels vary slightly by SPP version. The flow below matches the
> current administration guide; consult the
> [SPP Administration Guide](https://docs.oneidentity.com/bundle/safeguard-for-privileged-passwords_administration-guide_8.0/page/guides/administrationguide/settings-external-a2a-add.htm)
> for your version.

## Prerequisites

- Administrator access to SPP (typically **Asset Administrator** and
  **User Administrator** roles).
- The target assets and accounts already onboarded and managed in SPP.
- A client certificate **with its private key** for the provider's identity. The
  issuing CA must be trusted by SPP.

## 1. Trust the client certificate's issuing CA

Upload the CA that issued your client certificate:

**Settings → Security → Trusted CA Certificates → Add**

## 2. Create a certificate user

Create a user that SPP authenticates by certificate thumbprint:

**Users → New User → Authentication type: Certificate**

Assign the **thumbprint** of your client certificate to this user. The thumbprint
must match the certificate the provider presents exactly.

## 3. Confirm the accounts are managed

Ensure the asset accounts whose passwords, SSH keys, or API keys you want to
retrieve are onboarded as managed accounts in SPP.

## 4. Create the A2A registration

**External Integration → Application to Application → New Registration**

- **Name** — this is the value you put in the `SecretProviderClass` `appName`
  parameter.
- **Certificate User** — select the certificate user from step 2.
- **Retrievable Accounts** — add each account this registration may retrieve, and
  the credential type (password, SSH key, or API key).
- **IP restrictions** *(optional)* — if set, all retrievals must originate from an
  allowed address. In Kubernetes this is typically the node IP, because the
  provider DaemonSet runs with `hostNetwork: true`.

Each retrievable account is issued its own **API key** within the registration.
The provider discovers these automatically; you do not need to copy them into the
cluster.

## 5. Prepare the client certificate for Kubernetes

Export the certificate and key as PEM (convert from PKCS#12 if needed):

```bash
openssl pkcs12 -in client.pfx -clcerts -nokeys  -out clientCertificate.pem
openssl pkcs12 -in client.pfx -nocerts  -nodes  -out clientKey.pem
```

Create the node-publish secret:

```bash
kubectl create secret generic safeguard-a2a \
  --from-file=clientCertificate=clientCertificate.pem \
  --from-file=clientKey=clientKey.pem
kubectl label secret safeguard-a2a secrets-store.csi.k8s.io/used=true
```

## 6. Capture the appliance's issuing CA

So the provider can verify the appliance, export the CA chain that issued the
**appliance's** TLS certificate (this is separate from the client certificate CA
in step 1) and supply it as `safeguardCaBundle` in the `SecretProviderClass`.

## Mapping to provider configuration

| Safeguard concept | Provider setting |
| --- | --- |
| A2A registration name | `appName` |
| Appliance hostname | `safeguardHost` |
| Certificate user's certificate | `clientCertificate` / `clientKey` secrets |
| Appliance TLS issuing CA | `safeguardCaBundle` |
| Retrievable account | one file per account in the mount |
| Retrievable credential type | `objectType` (`Password` / `PrivateKey` / `ApiKey`) |

## Verify

After applying a `SecretProviderClass` and mounting it into a test pod
(see [usage](./usage.md)), the mounted files should contain the account
credentials. If retrieval fails, see [troubleshooting](./troubleshooting.md).
