package provider

import (
	"bytes"
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
	accountFilter := parseNameSet(attrib["accountNames"])
	podName := strings.TrimSpace(attrib["csi.storage.k8s.io/pod.name"])
	podNamespace := strings.TrimSpace(attrib["csi.storage.k8s.io/pod.namespace"])

	out, err := parseOutputConfig(attrib)
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
	bundle := make(map[string]accountBundle)
	matched := 0
	for _, account := range accounts {
		if appName != "" && account.ApplicationName != appName {
			continue
		}
		if accountFilter != nil {
			if _, ok := accountFilter[strings.ToLower(account.AccountName)]; !ok {
				continue
			}
		}
		matched++

		klog.Infof("Looking up %s", account.AccountName)

		if out.bundle {
			bundle[account.AccountName] = retrieveAccountBundle(ctx, a2a, account, out.objectTypes, out.keyFormat)
			objectVersionMap[strconv.Itoa(account.AccountID)] = uuid.New().String()
			continue
		}

		cred, err := retrieveCredential(ctx, a2a, account, out.objectTypes[0], out.keyFormat)
		if err != nil {
			// In file-per-account mode a miss means this account produces no
			// file at all, so surface it at Warning: it may be an expected
			// "credential type not available for this account", but it may also
			// be a genuine retrieval failure worth investigating.
			klog.Warningf("Could not fetch secret %s because %s", account.AccountName, err.Error())
			continue
		}

		// TODO: We should figure out how to grab a proper version
		objectVersionMap[strconv.Itoa(account.AccountID)] = uuid.New().String()
		files[account.AccountName] = cred

		klog.InfoS("added file to the gRPC response", "file", account.AccountName, "pod", klog.ObjectRef{Namespace: podNamespace, Name: podName})
	}

	if matched == 0 && (appName != "" || accountFilter != nil) {
		err := fmt.Errorf("no retrievable accounts matched (appName=%q, accountNames=%q)",
			appName, strings.TrimSpace(attrib["accountNames"]))
		klog.Error(err)
		return files, objectVersionMap, err
	}

	if out.bundle {
		data, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			klog.Error(err)
			return files, objectVersionMap, err
		}
		files[out.bundleFile] = data
		klog.InfoS("added bundle to the gRPC response", "file", out.bundleFile, "accounts", matched, "pod", klog.ObjectRef{Namespace: podNamespace, Name: podName})
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

// outputConfig captures the resolved output shaping for a mount request: whether
// to write one file per account or a single consolidated JSON bundle, which
// object types to retrieve, and the SSH key format.
type outputConfig struct {
	bundle      bool
	bundleFile  string
	objectTypes []string
	keyFormat   safeguard.KeyFormat
}

// parseOutputConfig resolves the outputFormat, objectType(s), keyFormat, and
// bundleFile attributes into an outputConfig.
//
// outputFormat defaults to "file-per-account": one file per account, named after
// the account, carrying the single objectType (default Password). "bundle"
// writes a single JSON file (bundleFile, default secrets.json) keyed by account
// name, carrying every objectTypes value for each account.
func parseOutputConfig(attrib map[string]string) (outputConfig, error) {
	keyFormat, err := parseKeyFormat(attrib["keyFormat"])
	if err != nil {
		return outputConfig{}, err
	}

	switch strings.ToLower(strings.TrimSpace(attrib["outputFormat"])) {
	case "", "file", "files", "file-per-account", "fileperaccount":
		objectType, err := normalizeObjectType(attrib["objectType"])
		if err != nil {
			return outputConfig{}, err
		}
		return outputConfig{objectTypes: []string{objectType}, keyFormat: keyFormat}, nil

	case "bundle", "json":
		objectTypes, err := parseObjectTypes(attrib)
		if err != nil {
			return outputConfig{}, err
		}
		bundleFile := strings.TrimSpace(attrib["bundleFile"])
		if bundleFile == "" {
			bundleFile = "secrets.json"
		}
		if strings.ContainsAny(bundleFile, `/\`) {
			return outputConfig{}, fmt.Errorf("bundleFile %q must be a plain file name without path separators", bundleFile)
		}
		return outputConfig{bundle: true, bundleFile: bundleFile, objectTypes: objectTypes, keyFormat: keyFormat}, nil

	default:
		return outputConfig{}, fmt.Errorf("unsupported outputFormat %q (expected file-per-account or bundle)",
			strings.TrimSpace(attrib["outputFormat"]))
	}
}

// normalizeObjectType resolves a single objectType value (defaulting to Password)
// to its canonical Safeguard spelling.
func normalizeObjectType(raw string) (string, error) {
	objectType := strings.TrimSpace(raw)
	if objectType == "" {
		objectType = "Password"
	}
	switch strings.ToLower(objectType) {
	case "password":
		return "Password", nil
	case "privatekey":
		return "PrivateKey", nil
	case "apikey":
		return "ApiKey", nil
	default:
		return "", fmt.Errorf("unsupported objectType %q (expected Password, PrivateKey, or ApiKey)", objectType)
	}
}

// parseObjectTypes resolves the objectTypes attribute (a comma-separated list)
// used by bundle mode, falling back to the single objectType attribute and then
// to Password. Duplicates are dropped while order is preserved.
func parseObjectTypes(attrib map[string]string) ([]string, error) {
	raw := strings.TrimSpace(attrib["objectTypes"])
	if raw == "" {
		objectType, err := normalizeObjectType(attrib["objectType"])
		if err != nil {
			return nil, err
		}
		return []string{objectType}, nil
	}

	seen := make(map[string]struct{})
	var types []string
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		objectType, err := normalizeObjectType(part)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[objectType]; ok {
			continue
		}
		seen[objectType] = struct{}{}
		types = append(types, objectType)
	}
	if len(types) == 0 {
		return nil, fmt.Errorf("objectTypes %q contained no valid object types", raw)
	}
	return types, nil
}

// parseNameSet parses a comma-separated attribute into a lowercased lookup set,
// returning nil when empty (meaning "no filter"). Account-name matching is
// case-insensitive.
func parseNameSet(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	set := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			set[strings.ToLower(part)] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
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

// accountBundle is one account's entry in a consolidated JSON bundle. Only the
// requested object types are populated; the rest are omitted.
type accountBundle struct {
	Password   *string         `json:"password,omitempty"`
	PrivateKey *string         `json:"privateKey,omitempty"`
	APIKey     json.RawMessage `json:"apiKey,omitempty"`
}

// retrieveAccountBundle fetches each requested object type for a single account
// and assembles them into an accountBundle. Accounts are commonly heterogeneous:
// a given account may carry only a password while the mount requests several
// types, so a per-type miss is expected rather than exceptional. A failed or
// absent type is logged at an informational level and omitted (the struct fields
// use omitempty), so a partial bundle is still returned and the mount succeeds.
func retrieveAccountBundle(ctx context.Context, a2a *safeguard.A2AContext, account safeguard.A2ARetrievableAccount, objectTypes []string, keyFormat safeguard.KeyFormat) accountBundle {
	var bundle accountBundle
	for _, objectType := range objectTypes {
		cred, err := retrieveCredential(ctx, a2a, account, objectType, keyFormat)
		if err != nil {
			// Expected miss: this account simply does not carry this type. Log
			// for awareness at info, not error, so a normal heterogeneous setup
			// does not trip error-based alerting.
			klog.Infof("Skipping %s for %s: not available (%s)", objectType, account.AccountName, err.Error())
			continue
		}
		switch objectType {
		case "Password":
			s := string(cred)
			bundle.Password = &s
		case "PrivateKey":
			s := string(cred)
			bundle.PrivateKey = &s
		case "ApiKey":
			bundle.APIKey = json.RawMessage(cred)
		}
	}
	return bundle
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
		// Safeguard returns key material with Windows CRLF line endings.
		// Normalize to LF so Linux pods (and stricter PEM/key parsers) can
		// consume the mounted key cleanly. This only rewrites line endings,
		// which are not semantically significant in PEM/SSH2/PuTTY key formats.
		return normalizeLineEndings(secret.Expose()), nil
	case "ApiKey":
		return retrieveAPIKeySecret(ctx, a2a, account.APIKey)
	default:
		return nil, fmt.Errorf("unsupported objectType %q", objectType)
	}
}

// normalizeLineEndings converts CRLF and lone CR line endings to LF. It is used
// for key material, which Safeguard returns with Windows CRLF endings, so the
// bytes written to a pod mount are clean for Linux consumers.
func normalizeLineEndings(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	return b
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
