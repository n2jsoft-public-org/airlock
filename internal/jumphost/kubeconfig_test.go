package jumphost

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testEKSEndpoint = "ABCD.gr7.eu-west-3.eks.amazonaws.com"

// installEKSAWS puts an aws executable on PATH answering what Prepare asks for an EKS cluster.
func installEKSAWS(t *testing.T, caData string) {
	t.Helper()
	dir := t.TempDir()
	cluster := `{"name":"platform","endpoint":"https://` + testEKSEndpoint + `","certificateAuthority":{"data":"` + caData + `"}}`
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" +
		"*describe-instances*) echo i-0123 ;;\n" +
		"*describe-cluster*) echo '" + cluster + "' ;;\n" +
		"esac\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func prepareEKS(t *testing.T, kubeconfigDir string) *Session {
	t.Helper()
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, KubeconfigDir: kubeconfigDir})
	session, err := runner.Prepare(context.Background(), Options{ResourceType: "eks", Name: "platform", AwsProfile: "dev", AwsRegion: "eu-west-3", LocalPortNumber: 15443, SkipIDEDataSource: true})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestPrepareEKSWritesSessionKubeconfig(t *testing.T) {
	installEKSAWS(t, "LS0tLS1CRUdJTg==")
	dir := t.TempDir()
	session := prepareEKS(t, dir)

	path := session.Details.Kubeconfig
	if filepath.Dir(path) != dir {
		t.Fatalf("kubeconfig = %q, want a file in %s", path, dir)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"server": "https://localhost:15443"`, `"tls-server-name": "` + testEKSEndpoint + `"`, `"certificate-authority-data": "LS0tLS1CRUdJTg=="`, `"get-token"`, `"platform"`, `"--profile"`, `"dev"`, `"eu-west-3"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("kubeconfig lacks %s:\n%s", want, raw)
		}
	}

	intro := strings.Join(session.Intro, "\n")
	for _, want := range []string{"export KUBECONFIG=" + path, "kubectl --server https://localhost:15443 --tls-server-name " + testEKSEndpoint} {
		if !strings.Contains(intro, want) {
			t.Fatalf("intro lacks %q:\n%s", want, intro)
		}
	}

	if message := session.Release(); !strings.Contains(message, "kubeconfig") {
		t.Fatalf("release message = %q, want the kubeconfig removal named", message)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat = %v, want the kubeconfig removed with the session", err)
	}
}

func TestPrepareEKSWithoutKubeconfigDirStillHintsKubectl(t *testing.T) {
	installEKSAWS(t, "LS0tLS1CRUdJTg==")
	session := prepareEKS(t, "")
	defer session.Release()

	if session.Details.Kubeconfig != "" {
		t.Fatalf("kubeconfig = %q, want none without a directory", session.Details.Kubeconfig)
	}
	if intro := strings.Join(session.Intro, "\n"); !strings.Contains(intro, "--tls-server-name "+testEKSEndpoint) {
		t.Fatalf("intro = %q, want the kubectl override", intro)
	}
}

func TestPrepareEKSWithoutCertificateAuthoritySkipsKubeconfig(t *testing.T) {
	installEKSAWS(t, "")
	dir := t.TempDir()
	session := prepareEKS(t, dir)
	defer session.Release()

	if session.Details.Kubeconfig != "" {
		t.Fatalf("kubeconfig = %q, want none: kubectl could not trust the cluster", session.Details.Kubeconfig)
	}
}
