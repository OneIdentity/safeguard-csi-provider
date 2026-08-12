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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/OneIdentity/safeguard-csi-provider/test/harness"
)

var b64 = b64pkg.StdEncoding

// TestE2EMount provisions a Safeguard account with a known password, mounts it
// into a pod through the CSI driver + provider, and asserts the mounted file
// contains exactly that password.
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

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
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
	kc.mustApply(ctx, manifests(namespace, cfg, fixture))

	// The pod only becomes Ready once the CSI mount (and thus retrieval) succeeds.
	if _, waitErr := kc.run(ctx, nil, "wait", "--namespace", namespace,
		"--for=condition=Ready", "pod/"+podName, "--timeout=180s"); waitErr != nil {
		dumpDiagnostics(ctx, t, kc, namespace)
		t.Fatalf("consumer pod did not become Ready: %v", waitErr)
	}

	got := kc.mustRun(ctx, nil, "exec", "--namespace", namespace, podName, "--",
		"cat", "/mnt/secrets/"+fixture.AccountName)
	if strings.TrimRight(got, "\n") != fixture.ExpectedPassword {
		t.Fatalf("mounted secret does not match the provisioned password")
	}
}

const podName = "safeguard-e2e-consumer"

// manifests renders the per-run Kubernetes objects: the node-publish secret
// carrying the A2A client certificate, the SecretProviderClass describing the
// retrieval, and a consumer pod that mounts the CSI volume.
func manifests(namespace string, cfg *harness.Config, fixture *harness.Fixture) string {
	insecure := "false"
	if cfg.Insecure {
		insecure = "true"
	}
	return fmt.Sprintf(`
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
---
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: safeguard
  namespace: %[1]s
spec:
  provider: safeguard
  parameters:
    safeguardHost: %[4]s
    appName: %[5]s
    objectType: Password
    insecureSkipVerify: %[6]q
---
apiVersion: v1
kind: Pod
metadata:
  name: %[7]s
  namespace: %[1]s
spec:
  restartPolicy: Never
  containers:
    - name: consumer
      image: registry.k8s.io/e2e-test-images/busybox:1.29-4
      command: ["/bin/sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: secrets
          mountPath: /mnt/secrets
          readOnly: true
  volumes:
    - name: secrets
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes:
          secretProviderClass: safeguard
        nodePublishSecretRef:
          name: safeguard-a2a
`,
		namespace,
		base64(fixture.PKI.ClientCertPEM),
		base64(fixture.PKI.ClientKeyPEM),
		cfg.Host,
		fixture.AppName,
		insecure,
		podName,
	)
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
func dumpDiagnostics(ctx context.Context, t *testing.T, kc *kubectl, namespace string) {
	t.Helper()
	if out, err := kc.run(ctx, nil, "describe", "pod", podName, "--namespace", namespace); err == nil {
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
