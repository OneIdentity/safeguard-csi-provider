package harness

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	safeguard "github.com/OneIdentity/safeguard-go"
	"golang.org/x/crypto/ssh"
)

// Fixture is the result of provisioning a complete A2A retrieval scenario on a
// live appliance. A single account holds a password, an SSH key, and an API key;
// the retrieval type is chosen by the SDK method at retrieval time, so one
// registered account serves all three Layer 0 tests.
type Fixture struct {
	// AppName is the A2A registration application name. It maps directly to the
	// provider's `appName` mount attribute.
	AppName string

	// PKI is the client certificate chain used for A2A authentication. Its CA is
	// uploaded to Safeguard and its thumbprint identifies the certificate user.
	PKI *ClientPKI

	// AccountName is the retrievable asset account's name (the file name the
	// provider writes for this object).
	AccountName string

	// ExpectedPassword is the password set on the account, so a live test can
	// assert the retrieved value matches exactly.
	ExpectedPassword string

	// ExpectedPublicKey is the OpenSSH authorized-keys line for the installed SSH
	// key, so a test can confirm the retrieved private key matches this public key.
	ExpectedPublicKey string
}

// AdminRoles are the least-privilege administrative roles the dedicated test
// admin needs: assets/accounts (AssetAdmin), the certificate user (UserAdmin),
// the A2A registration (PolicyAdmin), and the trusted CA upload (ApplianceAdmin).
var AdminRoles = []string{"AssetAdmin", "UserAdmin", "PolicyAdmin", "ApplianceAdmin"}

// ProvisionA2AFixture stands up an end-to-end A2A retrieval scenario against a
// live appliance and returns the fixture plus a teardown function. The teardown
// removes everything in reverse order and, unless Config.Keep is set, deletes the
// dedicated test admin last. Both the bootstrap client and the test-admin client
// are closed by teardown.
//
// Flow:
//  1. bootstrap: create a dedicated least-privilege admin, set its password
//  2. reconnect as that admin (all remaining objects are owned by it)
//  3. create an asset (generic "Other" platform) and an account
//  4. set the account password, install an SSH key, mint an API key
//  5. upload the client CA as a trusted certificate
//  6. create the certificate user bound to the client thumbprint
//  7. create the A2A registration and add the account as retrievable
func ProvisionA2AFixture(ctx context.Context, cfg *Config) (fixture *Fixture, teardown func(context.Context), err error) {
	pki, err := GenerateClientPKI(cfg.Name("client"))
	if err != nil {
		return nil, nil, fmt.Errorf("generating client PKI: %w", err)
	}

	boot, err := ConnectBootstrap(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	// If anything below fails, tear down whatever we managed to create so a
	// failed run does not leave the appliance dirty.
	var admin *SPP
	teardown = func(ctx context.Context) {
		if admin != nil {
			_ = admin.Cleanup(ctx)
			admin.Close(ctx)
		}
		_ = boot.Cleanup(ctx)
		boot.Close(ctx)
	}
	defer func() {
		if err != nil {
			teardown(ctx)
			teardown = nil
		}
	}()

	// Step 1: dedicated admin.
	adminName := cfg.Name("admin")
	adminPassword, err := randomPassword()
	if err != nil {
		return nil, nil, err
	}
	adminID, err := boot.createUser(ctx, map[string]any{
		"Name":                      adminName,
		"AdminRoles":                AdminRoles,
		"ChangePasswordAtNextLogin": false,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating test admin: %w", err)
	}
	boot.deferCleanup(fmt.Sprintf("user %q (id %d)", adminName, adminID), func(ctx context.Context) error {
		return boot.delete(ctx, fmt.Sprintf("Users/%d", adminID))
	})
	if err = boot.setPassword(ctx, fmt.Sprintf("Users/%d/Password", adminID), adminPassword); err != nil {
		return nil, nil, fmt.Errorf("setting test admin password: %w", err)
	}

	// Step 2: reconnect as the dedicated admin.
	admin, err = connectAs(ctx, cfg, "local", adminName, adminPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting as test admin %q: %w", adminName, err)
	}

	// Step 3: asset + account.
	platformID, err := admin.otherPlatformID(ctx)
	if err != nil {
		return nil, nil, err
	}
	assetName := cfg.Name("asset")
	assetID, err := admin.createAsset(ctx, assetName, platformID)
	if err != nil {
		return nil, nil, fmt.Errorf("creating asset: %w", err)
	}
	admin.deferCleanup(fmt.Sprintf("asset %q (id %d)", assetName, assetID), func(ctx context.Context) error {
		return admin.delete(ctx, fmt.Sprintf("Assets/%d", assetID))
	})

	accountName := cfg.Name("account")
	accountID, err := admin.createAccount(ctx, accountName, assetID)
	if err != nil {
		return nil, nil, fmt.Errorf("creating account: %w", err)
	}
	admin.deferCleanup(fmt.Sprintf("account %q (id %d)", accountName, accountID), func(ctx context.Context) error {
		return admin.delete(ctx, fmt.Sprintf("AssetAccounts/%d", accountID))
	})

	// Step 4: the three credentials on the single account.
	accountPassword, err := randomPassword()
	if err != nil {
		return nil, nil, err
	}
	if err = admin.setPassword(ctx, fmt.Sprintf("AssetAccounts/%d/Password", accountID), accountPassword); err != nil {
		return nil, nil, fmt.Errorf("setting account password: %w", err)
	}
	privatePEM, publicAuthorizedKey, err := generateSSHKey()
	if err != nil {
		return nil, nil, err
	}
	if err = admin.installSSHKey(ctx, accountID, privatePEM); err != nil {
		return nil, nil, fmt.Errorf("installing account SSH key: %w", err)
	}
	if err = admin.createAPIKey(ctx, accountID, cfg.Name("apikey")); err != nil {
		return nil, nil, fmt.Errorf("creating account API key: %w", err)
	}

	// Step 5: trust the client CA.
	trustedID, err := admin.uploadTrustedCertificate(ctx, base64.StdEncoding.EncodeToString(pki.CACertDER))
	if err != nil {
		return nil, nil, fmt.Errorf("uploading trusted CA: %w", err)
	}
	admin.deferCleanup(fmt.Sprintf("trusted certificate (id %d)", trustedID), func(ctx context.Context) error {
		return admin.delete(ctx, fmt.Sprintf("TrustedCertificates/%d", trustedID))
	})

	// Step 6: certificate user bound to the client thumbprint.
	certProviderID, err := admin.certificateProviderID(ctx)
	if err != nil {
		return nil, nil, err
	}
	certUserName := cfg.Name("certuser")
	certUserID, err := admin.createUser(ctx, map[string]any{
		"Name": certUserName,
		"PrimaryAuthenticationProvider": map[string]any{
			"Id":       certProviderID,
			"Identity": pki.Thumbprint,
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate user: %w", err)
	}
	admin.deferCleanup(fmt.Sprintf("cert user %q (id %d)", certUserName, certUserID), func(ctx context.Context) error {
		return admin.delete(ctx, fmt.Sprintf("Users/%d", certUserID))
	})

	// Step 7: A2A registration + retrievable account.
	appName := cfg.Name("app")
	registrationID, err := admin.createRegistration(ctx, appName, certUserID)
	if err != nil {
		return nil, nil, fmt.Errorf("creating A2A registration: %w", err)
	}
	admin.deferCleanup(fmt.Sprintf("A2A registration %q (id %d)", appName, registrationID), func(ctx context.Context) error {
		return admin.delete(ctx, fmt.Sprintf("A2ARegistrations/%d", registrationID))
	})
	if err = admin.addRetrievableAccount(ctx, registrationID, accountID); err != nil {
		return nil, nil, fmt.Errorf("adding retrievable account: %w", err)
	}

	fixture = &Fixture{
		AppName:           appName,
		PKI:               pki,
		AccountName:       accountName,
		ExpectedPassword:  accountPassword,
		ExpectedPublicKey: publicAuthorizedKey,
	}
	return fixture, teardown, nil
}

// connectAs logs in as an arbitrary provider\user with a password credential.
func connectAs(ctx context.Context, cfg *Config, provider, user, password string) (*SPP, error) {
	cred := safeguard.UsernamePassword(provider, user, safeguard.NewSecretString(password))
	client, err := safeguard.Connect(ctx, cfg.Host, cred, cfg.TLSOptions()...)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s as %s\\%s: %w", cfg.Host, provider, user, err)
	}
	return &SPP{cfg: cfg, client: client}, nil
}

// idResponse captures the Id that Safeguard returns from a create call.
type idResponse struct {
	Id int `json:"Id"`
}

// createUser creates a user from an arbitrary body and returns its Id.
func (s *SPP) createUser(ctx context.Context, body map[string]any) (int, error) {
	var out idResponse
	if err := s.postJSON(ctx, "Users", body, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}

// setPassword sets a password at a Password sub-resource. The Safeguard schema
// for these endpoints is a bare JSON string, so the password is sent as-is and
// the SDK JSON-encodes it to a quoted string.
func (s *SPP) setPassword(ctx context.Context, relURL, password string) error {
	return s.putJSON(ctx, relURL, password, nil)
}

// otherPlatformID finds the generic "Other" platform used for manually managed
// assets that require no network connectivity.
func (s *SPP) otherPlatformID(ctx context.Context) (int, error) {
	var platforms []struct {
		Id           int    `json:"Id"`
		Name         string `json:"Name"`
		PlatformType string `json:"PlatformType"`
	}
	if err := s.getJSON(ctx, "Platforms", &platforms, safeguard.WithQueryParam("filter", "Name eq 'Other'")); err != nil {
		return 0, fmt.Errorf("listing platforms: %w", err)
	}
	for _, p := range platforms {
		if p.Name == "Other" {
			return p.Id, nil
		}
	}
	if len(platforms) > 0 {
		return platforms[0].Id, nil
	}
	return 0, fmt.Errorf("no platform named %q found", "Other")
}

// createAsset creates a manually managed asset on the given platform and returns
// its Id.
func (s *SPP) createAsset(ctx context.Context, name string, platformID int) (int, error) {
	body := map[string]any{
		"Name":           name,
		"PlatformId":     platformID,
		"NetworkAddress": name + ".invalid",
	}
	var out idResponse
	if err := s.postJSON(ctx, "Assets", body, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}

// createAccount creates an asset account under the given asset and returns its Id.
func (s *SPP) createAccount(ctx context.Context, name string, assetID int) (int, error) {
	body := map[string]any{
		"Name":  name,
		"Asset": map[string]any{"Id": assetID},
	}
	var out idResponse
	if err := s.postJSON(ctx, "AssetAccounts", body, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}

// installSSHKey stores an SSH private key on the account so it can be retrieved
// over A2A.
func (s *SPP) installSSHKey(ctx context.Context, accountID int, privateKeyPEM string) error {
	body := map[string]any{
		"PrivateKey": privateKeyPEM,
		"KeyType":    "Rsa",
	}
	return s.postJSON(ctx, fmt.Sprintf("AssetAccounts/%d/InstallSshKey", accountID), map[string]any{
		"SshKeyToInstall": body,
	}, nil)
}

// createAPIKey mints an API key on the account. Safeguard generates the client
// identifier and secret; the A2A RetrieveAPIKey call returns them at test time.
func (s *SPP) createAPIKey(ctx context.Context, accountID int, name string) error {
	body := map[string]any{"Name": name}
	return s.postJSON(ctx, fmt.Sprintf("AssetAccounts/%d/ApiKeys", accountID), body, nil)
}

// uploadTrustedCertificate registers a CA certificate (base64 DER) as trusted so
// Safeguard accepts certificate users it issued. Returns the trusted cert Id.
func (s *SPP) uploadTrustedCertificate(ctx context.Context, base64DER string) (int, error) {
	body := map[string]any{"Base64CertificateData": base64DER}
	var out idResponse
	if err := s.postJSON(ctx, "TrustedCertificates", body, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}

// certificateProviderID finds the built-in certificate authentication provider.
func (s *SPP) certificateProviderID(ctx context.Context) (int, error) {
	var providers []struct {
		Id                int    `json:"Id"`
		TypeReferenceName string `json:"TypeReferenceName"`
	}
	if err := s.getJSON(ctx, "AuthenticationProviders", &providers); err != nil {
		return 0, fmt.Errorf("listing authentication providers: %w", err)
	}
	for _, p := range providers {
		if p.TypeReferenceName == "Certificate" {
			return p.Id, nil
		}
	}
	return 0, fmt.Errorf("no certificate authentication provider found")
}

// createRegistration creates an A2A registration owned by the certificate user
// and returns its Id.
func (s *SPP) createRegistration(ctx context.Context, appName string, certUserID int) (int, error) {
	body := map[string]any{
		"AppName":                   appName,
		"CertificateUserId":         certUserID,
		"VisibleToCertificateUsers": true,
	}
	var out idResponse
	if err := s.postJSON(ctx, "A2ARegistrations", body, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}

// addRetrievableAccount grants the registration retrieval access to the account.
func (s *SPP) addRetrievableAccount(ctx context.Context, registrationID, accountID int) error {
	body := map[string]any{"AccountId": accountID}
	return s.postJSON(ctx, fmt.Sprintf("A2ARegistrations/%d/RetrievableAccounts", registrationID), body, nil)
}

// randomPassword returns a strong, URL-safe random password.
func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating random password: %w", err)
	}
	// Prefix guarantees the value satisfies common complexity policies.
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// generateSSHKey creates an RSA key pair and returns the private key in PKCS#1
// PEM and the public key as an OpenSSH authorized-keys line.
func generateSSHKey() (privatePEM string, publicAuthorizedKey string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generating RSA key: %w", err)
	}
	privBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("deriving SSH public key: %w", err)
	}
	return string(privBytes), string(ssh.MarshalAuthorizedKey(pub)), nil
}
