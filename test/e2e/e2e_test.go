//go:build e2e

// Package e2e proves the full delivery path of the provider on a real
// Kubernetes node: the Secrets Store CSI Driver invokes the provider DaemonSet,
// which retrieves a credential from a live Safeguard appliance over A2A and
// writes it into a pod's mounted volume. The test asserts the mounted file
// equals the credential it provisioned on the appliance.
//
// It is guarded by the `e2e` build tag and never runs during `go test ./...`.
// The cluster, provider image, CSI driver, and provider DaemonSet must already
// be installed (see test/e2e/setup.sh and test/README.md); this test only
// provisions the per-run Safeguard fixture and Kubernetes objects, then asserts
// and tears them down.
package e2e

import (
	"bytes"
	"context"
	b64pkg "encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OneIdentity/safeguard-csi-provider/test/harness"
	"golang.org/x/crypto/ssh"
)

var b64 = b64pkg.StdEncoding

// TestE2EMount proves the full delivery path for every credential type. It
// provisions one Safeguard account carrying a password, an SSH private key, and
// an API key, then mounts them through the CSI driver + provider two ways and
// asserts a pod can cleanly interpret each:
//
//   - FilePerAccount: one file per object type across three CSI volumes;
//   - Bundle: a single consolidated JSON file carrying all three types.
//
// The multivalued accountNames filter is exercised live by pinning every mount
// to the provisioned account by name.
func TestE2EMount(t *testing.T) {
	cfg, err := harness.LoadConfig()
	if err != nil {
		t.Skipf("e2e test skipped: %v", err)
	}
	kubeconfig := envOrDefault("KUBECONFIG", "/etc/rancher/k3s/k3s.yaml")
	if _, statErr := os.Stat(kubeconfig); statErr != nil {
		t.Skipf("e2e test skipped: kubeconfig %q not found: %v", kubeconfig, statErr)
	}
	kc := &kubectl{t: t, kubeconfig: kubeconfig}

	// Preflight: the cluster must be reachable and both DaemonSets installed.
	kc.mustRun(context.Background(), nil, "get", "nodes")
	requireDaemonSetReady(t, kc, "kube-system", "csi-secrets-store-secrets-store-csi-driver",
		"Secrets Store CSI Driver is not installed; run test/e2e/setup.sh")
	requireDaemonSetReady(t, kc, "kube-system", "safeguard-csi-provider",
		"Safeguard CSI provider is not installed; run test/e2e/setup.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fixture, teardown, err := harness.ProvisionA2AFixture(ctx, cfg)
	if err != nil {
		t.Fatalf("provisioning A2A fixture: %v", err)
	}
	t.Cleanup(func() {
		tctx, tcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer tcancel()
		teardown(tctx)
	})

	namespace := cfg.RunID
	t.Cleanup(func() {
		// Best-effort namespace removal; ignore errors so SPP teardown still runs.
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		_, _ = kc.run(dctx, nil, "delete", "namespace", namespace, "--ignore-not-found", "--timeout=90s")
	})

	kc.mustRun(ctx, nil, "create", "namespace", namespace)
	// One node-publish secret (the A2A client certificate) shared by every mount.
	kc.mustApply(ctx, nodePublishSecret(namespace, fixture))

	insecure := strconv.FormatBool(cfg.Insecure)

	t.Run("FilePerAccount", func(t *testing.T) {
		pod := "consumer-files"
		kc.mustApply(ctx, filePerAccountManifests(namespace, pod, cfg, fixture, insecure))
		waitForPod(ctx, t, kc, namespace, pod)

		// Password: raw plaintext, byte-for-byte.
		if got := catMount(ctx, t, kc, namespace, pod, "/mnt/password/"+fixture.AccountName); got != fixture.ExpectedPassword {
			t.Fatalf("mounted password does not match the provisioned value")
		}
		// Private key: must parse as a usable SSH key whose public half matches.
		assertSSHKey(t, []byte(catMount(ctx, t, kc, namespace, pod, "/mnt/sshkey/"+fixture.AccountName)), fixture)
		// API key: must parse as JSON carrying the provisioned client secret.
		assertAPIKey(t, []byte(catMount(ctx, t, kc, namespace, pod, "/mnt/apikey/"+fixture.AccountName)), fixture)
	})

	t.Run("Bundle", func(t *testing.T) {
		pod := "consumer-bundle"
		kc.mustApply(ctx, bundleManifests(namespace, pod, cfg, fixture, insecure))
		waitForPod(ctx, t, kc, namespace, pod)

		raw := []byte(catMount(ctx, t, kc, namespace, pod, "/mnt/bundle/secrets.json"))
		var got map[string]struct {
			Password   *string         `json:"password"`
			PrivateKey *string         `json:"privateKey"`
			APIKey     json.RawMessage `json:"apiKey"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("bundle is not valid JSON: %v\n%s", err, raw)
		}
		entry, ok := got[fixture.AccountName]
		if !ok {
			t.Fatalf("bundle missing account %q: %s", fixture.AccountName, raw)
		}
		if entry.Password == nil || *entry.Password != fixture.ExpectedPassword {
			t.Fatalf("bundle password does not match the provisioned value")
		}
		if entry.PrivateKey == nil {
			t.Fatalf("bundle missing privateKey for %q", fixture.AccountName)
		}
		assertSSHKey(t, []byte(*entry.PrivateKey), fixture)
		assertAPIKey(t, entry.APIKey, fixture)
	})
}

// assertSSHKey verifies the mounted bytes parse as a usable SSH private key and
// that its public half matches the key provisioned on the appliance.
func assertSSHKey(t *testing.T, keyPEM []byte, fixture *harness.Fixture) {
	t.Helper()
	// The provider normalizes key material to LF, so the mounted key must carry
	// no carriage returns; a Linux pod can consume it directly.
	if bytes.ContainsRune(keyPEM, '\r') {
		t.Fatalf("mounted private key contains carriage returns; expected LF-only line endings")
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		t.Fatalf("mounted private key does not parse as an SSH key: %v", err)
	}
	got := bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	want := bytes.TrimSpace([]byte(fixture.ExpectedPublicKey))
	if !bytes.Equal(got, want) {
		t.Fatalf("mounted private key's public half does not match the provisioned key")
	}
}

// assertAPIKey verifies the mounted bytes parse as the provider's API-key JSON
// and carry the provisioned OAuth client secret for the expected client id.
func assertAPIKey(t *testing.T, raw []byte, fixture *harness.Fixture) {
	t.Helper()
	var keys []struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("mounted API key is not valid JSON: %v\n%s", err, raw)
	}
	for _, k := range keys {
		if k.ClientID == fixture.ExpectedClientID {
			if k.ClientSecret != fixture.ExpectedClientSecret {
				t.Fatalf("mounted API key client secret does not match the provisioned value")
			}
			return
		}
	}
	t.Fatalf("mounted API key JSON did not contain client id %q: %s", fixture.ExpectedClientID, raw)
}

// waitForPod blocks until the pod is Ready, which only happens once the CSI
// mount (and thus live retrieval) has succeeded. On timeout it dumps diagnostics.
func waitForPod(ctx context.Context, t *testing.T, kc *kubectl, namespace, pod string) {
	t.Helper()
	if _, err := kc.run(ctx, nil, "wait", "--namespace", namespace,
		"--for=condition=Ready", "pod/"+pod, "--timeout=180s"); err != nil {
		dumpDiagnostics(ctx, t, kc, namespace, pod)
		t.Fatalf("consumer pod %q did not become Ready: %v", pod, err)
	}
}

// catMount reads a mounted file from inside the pod, trimming a single trailing
// newline so exact comparisons are stable.
func catMount(ctx context.Context, t *testing.T, kc *kubectl, namespace, pod, path string) string {
	t.Helper()
	out := kc.mustRun(ctx, nil, "exec", "--namespace", namespace, pod, "--", "cat", path)
	return strings.TrimRight(out, "\n")
}

// nodePublishSecret renders the node-publish secret carrying the A2A client
// certificate and key, shared by every mount in the run.
func nodePublishSecret(namespace string, fixture *harness.Fixture) string {
	return fmt.Sprintf(`---
apiVersion: v1
kind: Secret
metadata:
  name: safeguard-a2a
  namespace: %[1]s
  labels:
    secrets-store.csi.k8s.io/used: "true"
type: Opaque
data:
  clientCertificate: %[2]s
  clientKey: %[3]s
`, namespace, base64(fixture.PKI.ClientCertPEM), base64(fixture.PKI.ClientKeyPEM))
}

// spcYAML renders one SecretProviderClass. extraParams supplies the object-type
// selection lines (already indented four spaces) that differ per mount.
func spcYAML(name, namespace string, cfg *harness.Config, fixture *harness.Fixture, insecure, extraParams string) string {
	return fmt.Sprintf(`---
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  provider: safeguard
  parameters:
    safeguardHost: %[3]s
    appName: %[4]s
    accountNames: %[5]s
    insecureSkipVerify: %[6]q
%[7]s
`, name, namespace, cfg.Host, fixture.AppName, fixture.AccountName, insecure, extraParams)
}

// csiVolume renders one CSI volume entry bound to a SecretProviderClass.
func csiVolume(volName, spcName string) string {
	return fmt.Sprintf(`    - name: %[1]s
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes:
          secretProviderClass: %[2]s
        nodePublishSecretRef:
          name: safeguard-a2a
`, volName, spcName)
}

// filePerAccountManifests renders three SecretProviderClasses (one per object
// type) and a pod that mounts each as its own CSI volume.
func filePerAccountManifests(namespace, pod string, cfg *harness.Config, fixture *harness.Fixture, insecure string) string {
	spcs := spcYAML("spc-password", namespace, cfg, fixture, insecure, "    objectType: Password") +
		spcYAML("spc-sshkey", namespace, cfg, fixture, insecure, "    objectType: PrivateKey\n    keyFormat: OpenSsh") +
		spcYAML("spc-apikey", namespace, cfg, fixture, insecure, "    objectType: ApiKey")

	podManifest := fmt.Sprintf(`---
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  restartPolicy: Never
  containers:
    - name: consumer
      image: registry.k8s.io/e2e-test-images/busybox:1.29-4
      command: ["/bin/sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: password
          mountPath: /mnt/password
          readOnly: true
        - name: sshkey
          mountPath: /mnt/sshkey
          readOnly: true
        - name: apikey
          mountPath: /mnt/apikey
          readOnly: true
  volumes:
%[3]s`,
		pod, namespace,
		csiVolume("password", "spc-password")+csiVolume("sshkey", "spc-sshkey")+csiVolume("apikey", "spc-apikey"))

	return spcs + podManifest
}

// bundleManifests renders a single bundle-mode SecretProviderClass carrying all
// three object types and a pod that mounts the consolidated JSON file.
func bundleManifests(namespace, pod string, cfg *harness.Config, fixture *harness.Fixture, insecure string) string {
	spc := spcYAML("spc-bundle", namespace, cfg, fixture, insecure,
		"    outputFormat: bundle\n    objectTypes: Password,PrivateKey,ApiKey\n    keyFormat: OpenSsh")

	podManifest := fmt.Sprintf(`---
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  restartPolicy: Never
  containers:
    - name: consumer
      image: registry.k8s.io/e2e-test-images/busybox:1.29-4
      command: ["/bin/sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: bundle
          mountPath: /mnt/bundle
          readOnly: true
  volumes:
%[3]s`,
		pod, namespace, csiVolume("bundle", "spc-bundle"))

	return spc + podManifest
}

// requireDaemonSetReady fails fast with an actionable message when a required
// DaemonSet is missing or never reaches a ready pod. It polls for a short window
// because DaemonSet pods can take a little time to report ready after the node
// (or the cluster) has just started.
func requireDaemonSetReady(t *testing.T, kc *kubectl, namespace, name, hint string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for {
		out, err := kc.run(context.Background(), nil, "get", "daemonset", name,
			"--namespace", namespace, "-o", "jsonpath={.status.numberReady}")
		if err != nil {
			lastErr = fmt.Errorf("daemonset %s/%s not found: %v", namespace, name, err)
		} else if ready := strings.TrimSpace(out); ready != "" && ready != "0" {
			return
		} else {
			lastErr = fmt.Errorf("daemonset %s/%s has no ready pods", namespace, name)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s (%v)", hint, lastErr)
		}
		time.Sleep(3 * time.Second)
	}
}

// dumpDiagnostics prints the information most useful for understanding a failed
// mount: pod events and the provider's recent logs.
func dumpDiagnostics(ctx context.Context, t *testing.T, kc *kubectl, namespace, pod string) {
	t.Helper()
	if out, err := kc.run(ctx, nil, "describe", "pod", pod, "--namespace", namespace); err == nil {
		t.Logf("pod description:\n%s", out)
	}
	if out, err := kc.run(ctx, nil, "logs", "--namespace", "kube-system",
		"-l", "app=safeguard-csi-provider", "--tail=50"); err == nil {
		t.Logf("provider logs:\n%s", out)
	}
}

// kubectl is a thin wrapper that runs kubectl against a specific kubeconfig.
type kubectl struct {
	t          *testing.T
	kubeconfig string
}

func (k *kubectl) run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+k.kubeconfig)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

func (k *kubectl) mustRun(ctx context.Context, stdin []byte, args ...string) string {
	k.t.Helper()
	out, err := k.run(ctx, stdin, args...)
	if err != nil {
		k.t.Fatalf("%v", err)
	}
	return out
}

func (k *kubectl) mustApply(ctx context.Context, manifest string) {
	k.t.Helper()
	k.mustRun(ctx, []byte(manifest), "apply", "-f", "-")
}

func base64(b []byte) string { return b64.EncodeToString(b) }

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
