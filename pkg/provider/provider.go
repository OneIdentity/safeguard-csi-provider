package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	safeguard "github.com/OneIdentity/safeguard-go"
	"github.com/google/uuid"
	"k8s.io/klog/v2"
)

// Provider retrieves secrets from Safeguard for Privileged Passwords over the
// Application-to-Application (A2A) service using the safeguard-go SDK.
type Provider struct {
}

// NewProvider creates a new provider
func NewProvider() *Provider {
	return &Provider{}
}

// MountSecretsStoreObjectContent mounts content of the secrets store object to target path
func (p *Provider) MountSecretsStoreObjectContent(ctx context.Context, attrib map[string]string, secrets map[string]string, targetPath string, permission os.FileMode) (map[string][]byte, map[string]string, error) {
	objectVersionMap := make(map[string]string)
	files := make(map[string][]byte)

	sgHost := strings.TrimSpace(attrib["safeguardHost"])
	appName := strings.TrimSpace(attrib["appName"])
	podName := strings.TrimSpace(attrib["csi.storage.k8s.io/pod.name"])
	podNamespace := strings.TrimSpace(attrib["csi.storage.k8s.io/pod.namespace"])

	objectType, keyFormat, err := parseObjectType(attrib)
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}

	clientCertificate := []byte(secrets["clientCertificate"])
	clientKey := []byte(secrets["clientKey"])

	a2a, err := newA2AContext(sgHost, attrib, clientCertificate, clientKey)
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}
	defer func() { _ = a2a.Close() }()

	// Enumerate every account this client certificate is registered to retrieve,
	// across all A2A registrations, each carrying its own per-account API key.
	accounts, err := a2a.GetRetrievableAccounts(ctx, "")
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}

	if len(accounts) == 0 {
		klog.Warning("No accounts were found")
	}

	// Filter to the requested application registration when appName is supplied.
	// An empty appName retrieves every account the certificate can access.
	matched := 0
	for _, account := range accounts {
		if appName != "" && account.ApplicationName != appName {
			continue
		}
		matched++

		klog.Infof("Looking up %s", account.AccountName)

		cred, err := retrieveCredential(ctx, a2a, account, objectType, keyFormat)
		if err != nil {
			klog.Errorf("Could not fetch secret %s because %s", account.AccountName, err.Error())
			continue
		}

		// TODO: We should figure out how to grab a proper version
		objectVersionMap[strconv.Itoa(account.AccountID)] = uuid.New().String()
		files[account.AccountName] = cred

		klog.InfoS("added file to the gRPC response", "file", account.AccountName, "pod", klog.ObjectRef{Namespace: podNamespace, Name: podName})
	}

	if appName != "" && matched == 0 {
		klog.Errorf("Requested app name %s had no retrievable accounts", appName)
		return files, objectVersionMap, fmt.Errorf("requested app name %s had no retrievable accounts", appName)
	}

	return files, objectVersionMap, nil
}

// newA2AContext builds an A2AContext from the client certificate and optional
// appliance-trust attributes. The certificate and key are supplied as separate
// PEM inputs, mirroring the clientCertificate/clientKey node-publish secrets.
func newA2AContext(sgHost string, attrib map[string]string, clientCertificate, clientKey []byte) (*safeguard.A2AContext, error) {
	opts := []safeguard.A2AOption{
		safeguard.WithA2APrivateKeyPEM(clientKey),
	}

	connOpts, err := connectionOptions(attrib)
	if err != nil {
		return nil, err
	}
	if len(connOpts) > 0 {
		opts = append(opts, safeguard.WithA2AConnectionOptions(connOpts...))
	}

	return safeguard.NewA2AContext(sgHost, clientCertificate, safeguard.Secret{}, opts...)
}

// connectionOptions translates the optional appliance-trust attributes into SDK
// connection options. safeguardCaBundle supplies a PEM CA bundle to trust a
// privately issued appliance certificate; insecureSkipVerify disables appliance
// certificate verification entirely and is intended only for bootstrapping.
func connectionOptions(attrib map[string]string) ([]safeguard.Option, error) {
	var connOpts []safeguard.Option

	if caBundle := strings.TrimSpace(attrib["safeguardCaBundle"]); caBundle != "" {
		connOpts = append(connOpts, safeguard.WithCABundle([]byte(caBundle)))
	}

	if raw := strings.TrimSpace(attrib["insecureSkipVerify"]); raw != "" {
		insecure, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid insecureSkipVerify value %q: %w", raw, err)
		}
		if insecure {
			klog.Warning("insecureSkipVerify is enabled; the Safeguard appliance certificate will NOT be verified")
			connOpts = append(connOpts, safeguard.WithInsecureTLS())
		}
	}

	return connOpts, nil
}

// parseObjectType resolves the optional objectType attribute (defaulting to
// Password) and, for private keys, the optional keyFormat attribute.
func parseObjectType(attrib map[string]string) (string, safeguard.KeyFormat, error) {
	objectType := strings.TrimSpace(attrib["objectType"])
	if objectType == "" {
		objectType = "Password"
	}

	switch strings.ToLower(objectType) {
	case "password":
		objectType = "Password"
	case "privatekey":
		objectType = "PrivateKey"
	case "apikey":
		objectType = "ApiKey"
	default:
		return "", "", fmt.Errorf("unsupported objectType %q (expected Password, PrivateKey, or ApiKey)", objectType)
	}

	keyFormat, err := parseKeyFormat(attrib["keyFormat"])
	if err != nil {
		return "", "", err
	}

	return objectType, keyFormat, nil
}

// parseKeyFormat resolves the optional keyFormat attribute used when retrieving
// SSH private keys. An empty value lets the SDK default to OpenSSH.
func parseKeyFormat(raw string) (safeguard.KeyFormat, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case "openssh":
		return safeguard.KeyFormatOpenSSH, nil
	case "ssh2":
		return safeguard.KeyFormatSSH2, nil
	case "putty":
		return safeguard.KeyFormatPuTTY, nil
	default:
		return "", fmt.Errorf("unsupported keyFormat %q (expected OpenSsh, Ssh2, or Putty)", raw)
	}
}

// retrieveCredential fetches the credential for a single account in the requested
// format and returns the plaintext bytes to write to the pod mount.
func retrieveCredential(ctx context.Context, a2a *safeguard.A2AContext, account safeguard.A2ARetrievableAccount, objectType string, keyFormat safeguard.KeyFormat) ([]byte, error) {
	switch objectType {
	case "Password":
		secret, err := a2a.RetrievePassword(ctx, account.APIKey)
		if err != nil {
			return nil, err
		}
		return secret.Expose(), nil
	case "PrivateKey":
		secret, err := a2a.RetrievePrivateKey(ctx, account.APIKey, keyFormat)
		if err != nil {
			return nil, err
		}
		return secret.Expose(), nil
	case "ApiKey":
		return retrieveAPIKeySecret(ctx, a2a, account.APIKey)
	default:
		return nil, fmt.Errorf("unsupported objectType %q", objectType)
	}
}

// retrieveAPIKeySecret fetches the API key credentials for an account and
// serializes them as JSON, exposing the client secret so it can be written to
// the pod mount. The SDK redacts Secret values when marshaled directly, so the
// exposed value is copied into a plain struct here.
func retrieveAPIKeySecret(ctx context.Context, a2a *safeguard.A2AContext, apiKey safeguard.Secret) ([]byte, error) {
	keys, err := a2a.RetrieveAPIKey(ctx, apiKey)
	if err != nil {
		return nil, err
	}

	type apiKeyJSON struct {
		ID             int    `json:"id"`
		Name           string `json:"name"`
		Description    string `json:"description"`
		ClientID       string `json:"clientId"`
		ClientSecret   string `json:"clientSecret"`
		ClientSecretID string `json:"clientSecretId"`
	}

	out := make([]apiKeyJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiKeyJSON{
			ID:             k.ID,
			Name:           k.Name,
			Description:    k.Description,
			ClientID:       k.ClientID,
			ClientSecret:   k.ClientSecret.ExposeString(),
			ClientSecretID: k.ClientSecretID,
		})
	}

	return json.Marshal(out)
}
