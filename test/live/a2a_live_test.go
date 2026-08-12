//go:build live

// Package live exercises Safeguard A2A credential retrieval against a real
// appliance. It is guarded by the `live` build tag so it never runs during an
// ordinary `go test ./...`; run it explicitly with `-tags live` and the
// SAFEGUARD_* environment variables set (see test/README.md).
package live

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/OneIdentity/safeguard-csi-provider/test/harness"
	safeguard "github.com/OneIdentity/safeguard-go"
	"golang.org/x/crypto/ssh"
)

// TestA2ARetrieval provisions a complete A2A scenario on a live appliance, then
// retrieves each credential type the provider supports (password, SSH key, API
// key) and asserts the values are correct. Everything provisioned is torn down
// afterward unless SAFEGUARD_KEEP is set.
func TestA2ARetrieval(t *testing.T) {
	cfg, err := harness.LoadConfig()
	if err != nil {
		t.Skipf("live test skipped: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fixture, teardown, err := harness.ProvisionA2AFixture(ctx, cfg)
	if err != nil {
		t.Fatalf("provisioning A2A fixture: %v", err)
	}
	t.Cleanup(func() {
		// Use a fresh context so teardown still runs if the test context expired.
		tctx, tcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer tcancel()
		teardown(tctx)
	})

	a2a, err := safeguard.NewA2AContext(
		cfg.Host,
		fixture.PKI.ClientCertPEM,
		safeguard.Secret{},
		safeguard.WithA2APrivateKeyPEM(fixture.PKI.ClientKeyPEM),
		safeguard.WithA2AConnectionOptions(cfg.TLSOptions()...),
	)
	if err != nil {
		t.Fatalf("building A2A context: %v", err)
	}
	defer a2a.Close()

	apiKey := retrievableAPIKey(ctx, t, a2a, fixture)

	t.Run("Password", func(t *testing.T) {
		got, err := a2a.RetrievePassword(ctx, apiKey)
		if err != nil {
			t.Fatalf("retrieving password: %v", err)
		}
		if got.ExposeString() != fixture.ExpectedPassword {
			t.Fatalf("retrieved password does not match the provisioned value")
		}
	})

	t.Run("PrivateKey", func(t *testing.T) {
		got, err := a2a.RetrievePrivateKey(ctx, apiKey, safeguard.KeyFormatOpenSSH)
		if err != nil {
			t.Fatalf("retrieving private key: %v", err)
		}
		signer, err := ssh.ParsePrivateKey(got.Expose())
		if err != nil {
			t.Fatalf("parsing retrieved private key: %v", err)
		}
		gotPub := ssh.MarshalAuthorizedKey(signer.PublicKey())
		wantPub := []byte(fixture.ExpectedPublicKey)
		if !bytes.Equal(bytes.TrimSpace(gotPub), bytes.TrimSpace(wantPub)) {
			t.Fatalf("retrieved private key does not match the provisioned public key")
		}
	})

	t.Run("APIKey", func(t *testing.T) {
		keys, err := a2a.RetrieveAPIKey(ctx, apiKey)
		if err != nil {
			t.Fatalf("retrieving API key: %v", err)
		}
		var got *safeguard.APIKey
		for i := range keys {
			if keys[i].ClientID == fixture.ExpectedClientID {
				got = &keys[i]
				break
			}
		}
		if got == nil {
			t.Fatalf("no retrieved API key matched client id %q (got %d keys)", fixture.ExpectedClientID, len(keys))
		}
		if got.ClientSecret.ExposeString() != fixture.ExpectedClientSecret {
			t.Fatalf("retrieved API key secret does not match the provisioned value")
		}
	})
}

// retrievableAPIKey looks up the A2A API key that authorizes retrieval of the
// provisioned account. The A2A API key is per-account and issued by the
// registration, distinct from the account's own API-key credential.
func retrievableAPIKey(ctx context.Context, t *testing.T, a2a *safeguard.A2AContext, fixture *harness.Fixture) safeguard.Secret {
	t.Helper()
	accounts, err := a2a.GetRetrievableAccounts(ctx, "")
	if err != nil {
		t.Fatalf("listing retrievable accounts: %v", err)
	}
	for _, acct := range accounts {
		if acct.AccountName == fixture.AccountName {
			return acct.APIKey
		}
	}
	t.Fatalf("account %q is not retrievable by app %q", fixture.AccountName, fixture.AppName)
	return safeguard.Secret{}
}
