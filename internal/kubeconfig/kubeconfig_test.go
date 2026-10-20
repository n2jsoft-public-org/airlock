package kubeconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testCluster() *Cluster {
	return &Cluster{
		Name:                     "airlock-dev-eu-west-3-platform",
		Server:                   "https://localhost:15443",
		TLSServerName:            "ABCD.gr7.eu-west-3.eks.amazonaws.com",
		CertificateAuthorityData: "LS0tLS1CRUdJTg==",
		Command:                  "airlock",
		Args:                     []string{"eks-token", "--cluster-name", "platform", "--profile", "dev", "--region", "eu-west-3"},
	}
}

func TestWriteGivesKubectlOneContextOnTheTunnel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kube")
	path, _, err := Write(dir, testCluster())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Ext(path) != ".json" {
		t.Fatalf("path = %q, want a .json file in %s", path, dir)
	}

	raw, err := os.ReadFile(path) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CurrentContext string `json:"current-context"`
		Clusters       []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server        string `json:"server"`
				TLSServerName string `json:"tls-server-name"`
				CAData        string `json:"certificate-authority-data"`
			} `json:"cluster"`
		} `json:"clusters"`
		Contexts []struct {
			Name    string `json:"name"`
			Context struct {
				Cluster string `json:"cluster"`
				User    string `json:"user"`
			} `json:"context"`
		} `json:"contexts"`
		Users []struct {
			Name string `json:"name"`
			User struct {
				Exec struct {
					APIVersion      string   `json:"apiVersion"`
					Command         string   `json:"command"`
					InteractiveMode string   `json:"interactiveMode"`
					Args            []string `json:"args"`
				} `json:"exec"`
			} `json:"user"`
		} `json:"users"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("kubeconfig is not valid JSON: %v", err)
	}
	name := "airlock-dev-eu-west-3-platform"
	if got.CurrentContext != name || len(got.Contexts) != 1 || got.Contexts[0].Context.Cluster != name || got.Contexts[0].Context.User != name {
		t.Fatalf("contexts = %+v current = %q, want one current context %s", got.Contexts, got.CurrentContext, name)
	}
	cluster := got.Clusters[0].Cluster
	if cluster.Server != "https://localhost:15443" || cluster.TLSServerName != "ABCD.gr7.eu-west-3.eks.amazonaws.com" || cluster.CAData != "LS0tLS1CRUdJTg==" {
		t.Fatalf("cluster = %+v, want the tunnel checked against the real endpoint", cluster)
	}
	exec := got.Users[0].User.Exec
	if exec.APIVersion != "client.authentication.k8s.io/v1beta1" || exec.Command != "airlock" || len(exec.Args) != 7 || exec.InteractiveMode != "Never" {
		t.Fatalf("exec = %+v, want airlock eks-token", exec)
	}
}

func TestWriteKeepsTheFilePrivate(t *testing.T) {
	path, _, err := Write(t.TempDir(), testCluster())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestWriteNamesTheFileSafely(t *testing.T) {
	cluster := testCluster()
	cluster.Name = "airlock-team/prod-eu-west-1-../api"
	dir := t.TempDir()
	path, _, err := Write(dir, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != "airlock-team-prod-eu-west-1-..-api.json" {
		t.Fatalf("path = %q, want a file directly in %s", path, dir)
	}
}

func TestRemoveDeletesTheFileOnce(t *testing.T) {
	path, remove, err := Write(t.TempDir(), testCluster())
	if err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat = %v, want the kubeconfig removed", err)
	}
	if err := remove(); err != nil {
		t.Fatalf("second remove = %v, want no error", err)
	}
}
