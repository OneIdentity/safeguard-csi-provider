// Package harness provides the shared building blocks for the live and end-to-end
// test suites: environment-driven configuration, a self-contained PKI generator
// for A2A client certificates, and Safeguard provisioning/cleanup helpers.
//
// Nothing in this package runs on its own. The live and e2e suites are guarded by
// build tags (`live` and `e2e`), so `go test ./...` and CI stay hermetic.
package harness

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	safeguard "github.com/OneIdentity/safeguard-go"
)

// Config captures everything the live/e2e suites need to talk to a real
// Safeguard appliance. It is populated entirely from environment variables so
// no secrets are ever committed.
type Config struct {
	// Host is the Safeguard appliance hostname (no scheme, no path).
	Host string

	// Bootstrap credentials used to create a dedicated test administrator.
	BootstrapProvider string
	BootstrapUser     string
	BootstrapPassword string

	// Appliance TLS trust. Provide a CA bundle file to verify a privately issued
	// appliance certificate, or set Insecure to skip verification for a
	// throwaway test appliance.
	CABundlePEM []byte
	Insecure    bool

	// Keep leaves all provisioned Safeguard objects in place after a run for
	// debugging instead of deleting them.
	Keep bool

	// RunID uniquely tags every object a run creates so cleanup is precise and
	// concurrent runs never collide.
	RunID string
}

// Environment variable names understood by the test harness.
const (
	EnvHost              = "SAFEGUARD_HOST"
	EnvBootstrapProvider = "SAFEGUARD_BOOTSTRAP_PROVIDER"
	EnvBootstrapUser     = "SAFEGUARD_BOOTSTRAP_USER"
	EnvBootstrapPassword = "SAFEGUARD_BOOTSTRAP_PASSWORD"
	EnvCABundleFile      = "SAFEGUARD_CA_BUNDLE_FILE"
	EnvInsecure          = "SAFEGUARD_INSECURE"
	EnvKeep              = "SAFEGUARD_KEEP"
	EnvRunID             = "SAFEGUARD_RUN_ID"
)

// LoadConfig reads the harness configuration from the environment. It returns an
// error listing every missing required value so the suite can Skip with a clear
// message instead of failing cryptically.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		Host:              strings.TrimSpace(os.Getenv(EnvHost)),
		BootstrapProvider: envOrDefault(EnvBootstrapProvider, "local"),
		BootstrapUser:     envOrDefault(EnvBootstrapUser, "admin"),
		BootstrapPassword: os.Getenv(EnvBootstrapPassword),
		Insecure:          envBool(EnvInsecure),
		Keep:              envBool(EnvKeep),
		RunID:             strings.TrimSpace(os.Getenv(EnvRunID)),
	}

	var missing []string
	if cfg.Host == "" {
		missing = append(missing, EnvHost)
	}
	if cfg.BootstrapPassword == "" {
		missing = append(missing, EnvBootstrapPassword)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	if path := strings.TrimSpace(os.Getenv(EnvCABundleFile)); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s=%q: %w", EnvCABundleFile, path, err)
		}
		cfg.CABundlePEM = pem
	}

	if cfg.RunID == "" {
		cfg.RunID = fmt.Sprintf("csi-e2e-%d", time.Now().Unix())
	}

	return cfg, nil
}

// TLSOptions returns the SDK connection options that apply the configured
// appliance trust. The same options are used for the bootstrap admin client and
// for the A2A retrieval context, so both trust the appliance identically.
func (c *Config) TLSOptions() []safeguard.Option {
	var opts []safeguard.Option
	if len(c.CABundlePEM) > 0 {
		opts = append(opts, safeguard.WithCABundle(c.CABundlePEM))
	}
	if c.Insecure {
		opts = append(opts, safeguard.WithInsecureTLS())
	}
	return opts
}

// BootstrapCredential builds the SDK credential for the bootstrap administrator.
// It uses the PKCE headless flow rather than the Resource Owner Grant, which
// Safeguard appliances commonly disable.
func (c *Config) BootstrapCredential() safeguard.Credential {
	return safeguard.PKCEHeadless(
		c.BootstrapProvider,
		c.BootstrapUser,
		safeguard.NewSecretString(c.BootstrapPassword),
	)
}

// Name derives a unique, run-scoped object name from a short role, e.g.
// Name("asset") -> "csi-e2e-1699999999-asset".
func (c *Config) Name(role string) string {
	return fmt.Sprintf("%s-%s", c.RunID, role)
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}
