package harness

import (
	"context"
	"encoding/json"
	"fmt"

	safeguard "github.com/OneIdentity/safeguard-go"
)

// SPP wraps an authenticated Safeguard core-API client and tracks the objects a
// run creates so they can be torn down in reverse order. Provisioning methods
// live in spp_provision.go; this file owns connection, request plumbing, and
// cleanup.
type SPP struct {
	cfg    *Config
	client *safeguard.Client

	// cleanups are deferred deletions, run last-in-first-out so dependent objects
	// (e.g. an A2A registration) are removed before what they depend on (the cert
	// user, the account, the asset).
	cleanups []cleanup
}

type cleanup struct {
	what string
	fn   func(ctx context.Context) error
}

// ConnectBootstrap logs in with the bootstrap administrator credential. This is
// the entry point for a run; step 1 (creating a dedicated, least-privilege test
// admin) is performed by a provisioning method once connected.
func ConnectBootstrap(ctx context.Context, cfg *Config) (*SPP, error) {
	client, err := safeguard.Connect(ctx, cfg.Host, cfg.BootstrapCredential(), cfg.TLSOptions()...)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s as %s\\%s: %w", cfg.Host, cfg.BootstrapProvider, cfg.BootstrapUser, err)
	}
	return &SPP{cfg: cfg, client: client}, nil
}

// Client exposes the underlying SDK client for calls not yet wrapped by a typed
// provisioning method.
func (s *SPP) Client() *safeguard.Client { return s.client }

// getJSON issues a GET against the core service and decodes a successful JSON
// body into out.
func (s *SPP) getJSON(ctx context.Context, relURL string, out any, opts ...safeguard.ReqOption) error {
	resp, err := s.client.Get(ctx, safeguard.Core, relURL, opts...)
	if err != nil {
		return fmt.Errorf("GET %s: %w", relURL, err)
	}
	return decodeInto(fmt.Sprintf("GET %s", relURL), resp, out)
}

// postJSON issues a POST with a JSON body against the core service and decodes a
// successful JSON body into out (out may be nil to ignore the response).
func (s *SPP) postJSON(ctx context.Context, relURL string, body, out any, opts ...safeguard.ReqOption) error {
	resp, err := s.client.Post(ctx, safeguard.Core, relURL, body, opts...)
	if err != nil {
		return fmt.Errorf("POST %s: %w", relURL, err)
	}
	return decodeInto(fmt.Sprintf("POST %s", relURL), resp, out)
}

// putJSON issues a PUT with a JSON body against the core service and decodes a
// successful JSON body into out (out may be nil to ignore the response).
func (s *SPP) putJSON(ctx context.Context, relURL string, body, out any, opts ...safeguard.ReqOption) error {
	resp, err := s.client.Put(ctx, safeguard.Core, relURL, body, opts...)
	if err != nil {
		return fmt.Errorf("PUT %s: %w", relURL, err)
	}
	return decodeInto(fmt.Sprintf("PUT %s", relURL), resp, out)
}

// delete issues a DELETE against the core service and returns an error on a
// non-success status. A 404 is treated as already-deleted and ignored so
// cleanup is idempotent.
func (s *SPP) delete(ctx context.Context, relURL string) error {
	resp, err := s.client.Delete(ctx, safeguard.Core, relURL)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", relURL, err)
	}
	if resp.StatusCode == 404 {
		return nil
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("DELETE %s: unexpected status %d: %s", relURL, resp.StatusCode, resp.BodyString())
	}
	return nil
}

// deferCleanup records a teardown action to run during Cleanup.
func (s *SPP) deferCleanup(what string, fn func(ctx context.Context) error) {
	s.cleanups = append(s.cleanups, cleanup{what: what, fn: fn})
}

// Cleanup removes every object the run created, most-recently-created first.
// When Config.Keep is set it skips deletion and reports what was left behind.
// It returns a joined error describing any deletions that failed; callers should
// log rather than fail the test on cleanup errors.
func (s *SPP) Cleanup(ctx context.Context) error {
	if s.cfg.Keep {
		return nil
	}
	var errs []error
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		c := s.cleanups[i]
		if err := c.fn(ctx); err != nil {
			errs = append(errs, fmt.Errorf("cleanup %s: %w", c.what, err))
		}
	}
	s.cleanups = nil
	if len(errs) > 0 {
		return fmt.Errorf("%d cleanup error(s): %v", len(errs), errs)
	}
	return nil
}

// KeptObjects lists the objects left behind when Config.Keep is set, for logging.
func (s *SPP) KeptObjects() []string {
	names := make([]string, 0, len(s.cleanups))
	for _, c := range s.cleanups {
		names = append(names, c.what)
	}
	return names
}

// Close logs the client out of the appliance.
func (s *SPP) Close(ctx context.Context) {
	if s.client != nil {
		_ = s.client.Logout(ctx)
		_ = s.client.Close()
	}
}

// decodeInto validates a Response is 2xx and, when out is non-nil and a body is
// present, unmarshals the JSON body into out. Errors include the status and body
// so a failed provisioning call is diagnosable without a debugger.
func decodeInto(what string, resp safeguard.Response, out any) error {
	if !resp.IsSuccess() {
		return fmt.Errorf("%s: unexpected status %d: %s", what, resp.StatusCode, resp.BodyString())
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%s: decoding response body: %w", what, err)
	}
	return nil
}
