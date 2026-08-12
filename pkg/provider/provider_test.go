package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testAccount is a minimal representation of one retrievable account returned by
// the fake A2A appliance, keyed by the API key that authorizes its retrieval.
type testAccount struct {
	regID       int
	appName     string
	accountID   int
	accountName string
	apiKey      string
	password    string
	// failCredential, when true, makes the Credentials endpoint return 500 for
	// this account's API key.
	failCredential bool
}

// newFakeAppliance starts an httptest TLS server that mimics the subset of the
// Safeguard A2A/Core API the provider uses: listing registrations, listing an
// registration's retrievable accounts, and retrieving a password credential.
func newFakeAppliance(t *testing.T, accounts []testAccount) *httptest.Server {
	t.Helper()

	byAPIKey := make(map[string]testAccount)
	for _, a := range accounts {
		byAPIKey[a.apiKey] = a
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/A2ARegistrations"):
			regs := map[int]map[string]any{}
			for _, a := range accounts {
				regs[a.regID] = map[string]any{
					"Id":          a.regID,
					"AppName":     a.appName,
					"Description": "",
					"Disabled":    false,
				}
			}
			out := make([]map[string]any, 0, len(regs))
			for _, r := range regs {
				out = append(out, r)
			}
			writeJSON(w, out)

		case strings.HasSuffix(path, "/RetrievableAccounts"):
			// Path form: .../A2ARegistrations/{id}/RetrievableAccounts
			segs := strings.Split(strings.Trim(path, "/"), "/")
			regID := segs[len(segs)-2]
			out := []map[string]any{}
			for _, a := range accounts {
				if strconv.Itoa(a.regID) != regID {
					continue
				}
				out = append(out, map[string]any{
					"AccountId":   a.accountID,
					"AccountName": a.accountName,
					"ApiKey":      a.apiKey,
					"AssetName":   "asset-" + a.accountName,
				})
			}
			writeJSON(w, out)

		case strings.HasSuffix(path, "/Credentials"):
			apiKey := strings.TrimPrefix(r.Header.Get("Authorization"), "A2A ")
			acct, ok := byAPIKey[apiKey]
			if !ok || acct.failCredential {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeJSON(w, acct.password)

		default:
			http.NotFound(w, r)
		}
	})

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// baseAttrib returns the attribute map for a mount request against server,
// trusting the server's certificate via safeguardCaBundle.
func baseAttrib(t *testing.T, server *httptest.Server, appName string) map[string]string {
	t.Helper()
	return map[string]string{
		"safeguardHost":     strings.TrimPrefix(server.URL, "https://"),
		"appName":           appName,
		"safeguardCaBundle": string(serverCertPEM(t, server)),
	}
}

func serverCertPEM(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	cert := server.Certificate()
	if cert == nil {
		t.Fatal("server certificate is nil")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// clientCertSecrets generates a self-signed client certificate and returns the
// cert and key PEM as separate values, mirroring the clientCertificate and
// clientKey node-publish secrets.
func clientCertSecrets(t *testing.T) map[string]string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "safeguard-csi-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return map[string]string{
		"clientCertificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"clientKey":         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}
}

func TestMountPasswordFilteredByAppName(t *testing.T) {
	accounts := []testAccount{
		{regID: 1, appName: "app1", accountID: 10, accountName: "db-admin", apiKey: "key-10", password: "pw-10"},
		{regID: 2, appName: "app2", accountID: 20, accountName: "svc-account", apiKey: "key-20", password: "pw-20"},
	}
	server := newFakeAppliance(t, accounts)

	p := NewProvider()
	files, versions, err := p.MountSecretsStoreObjectContent(
		context.Background(),
		baseAttrib(t, server, "app1"),
		clientCertSecrets(t),
		"/tmp/target",
		os.FileMode(0o644),
	)
	if err != nil {
		t.Fatalf("mount returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(files), files)
	}
	if got := string(files["db-admin"]); got != "pw-10" {
		t.Fatalf("db-admin content = %q, want %q", got, "pw-10")
	}
	if _, ok := files["svc-account"]; ok {
		t.Fatal("svc-account should have been filtered out by appName")
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 version entry, got %d", len(versions))
	}
}

func TestMountEmptyAppNameRetrievesAll(t *testing.T) {
	accounts := []testAccount{
		{regID: 1, appName: "app1", accountID: 10, accountName: "db-admin", apiKey: "key-10", password: "pw-10"},
		{regID: 2, appName: "app2", accountID: 20, accountName: "svc-account", apiKey: "key-20", password: "pw-20"},
	}
	server := newFakeAppliance(t, accounts)

	p := NewProvider()
	files, _, err := p.MountSecretsStoreObjectContent(
		context.Background(),
		baseAttrib(t, server, ""),
		clientCertSecrets(t),
		"/tmp/target",
		os.FileMode(0o644),
	)
	if err != nil {
		t.Fatalf("mount returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestMountUnknownAppNameErrors(t *testing.T) {
	accounts := []testAccount{
		{regID: 1, appName: "app1", accountID: 10, accountName: "db-admin", apiKey: "key-10", password: "pw-10"},
	}
	server := newFakeAppliance(t, accounts)

	p := NewProvider()
	_, _, err := p.MountSecretsStoreObjectContent(
		context.Background(),
		baseAttrib(t, server, "does-not-exist"),
		clientCertSecrets(t),
		"/tmp/target",
		os.FileMode(0o644),
	)
	if err == nil {
		t.Fatal("expected error for unknown appName, got nil")
	}
}

func TestMountPerAccountFailureIsSkipped(t *testing.T) {
	accounts := []testAccount{
		{regID: 1, appName: "app1", accountID: 10, accountName: "db-admin", apiKey: "key-10", password: "pw-10"},
		{regID: 1, appName: "app1", accountID: 11, accountName: "broken", apiKey: "key-11", failCredential: true},
	}
	server := newFakeAppliance(t, accounts)

	p := NewProvider()
	files, _, err := p.MountSecretsStoreObjectContent(
		context.Background(),
		baseAttrib(t, server, "app1"),
		clientCertSecrets(t),
		"/tmp/target",
		os.FileMode(0o644),
	)
	if err != nil {
		t.Fatalf("mount returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 successful file, got %d: %v", len(files), files)
	}
	if _, ok := files["db-admin"]; !ok {
		t.Fatal("expected db-admin to be retrieved despite sibling failure")
	}
}

func TestMountInvalidObjectTypeErrors(t *testing.T) {
	server := newFakeAppliance(t, nil)

	attrib := baseAttrib(t, server, "app1")
	attrib["objectType"] = "Bogus"

	p := NewProvider()
	_, _, err := p.MountSecretsStoreObjectContent(
		context.Background(),
		attrib,
		clientCertSecrets(t),
		"/tmp/target",
		os.FileMode(0o644),
	)
	if err == nil {
		t.Fatal("expected error for invalid objectType, got nil")
	}
}

func TestParseKeyFormat(t *testing.T) {
	cases := map[string]bool{
		"":        true,
		"OpenSsh": true,
		"ssh2":    true,
		"PUTTY":   true,
		"bogus":   false,
	}
	for in, wantOK := range cases {
		_, err := parseKeyFormat(in)
		if wantOK && err != nil {
			t.Errorf("parseKeyFormat(%q) unexpected error: %v", in, err)
		}
		if !wantOK && err == nil {
			t.Errorf("parseKeyFormat(%q) expected error, got nil", in)
		}
	}
}
