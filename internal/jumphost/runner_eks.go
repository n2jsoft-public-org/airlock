package jumphost

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/kubeconfig"
)

func (r *Runner) prepareEKS(ctx context.Context, cfg awsConfig, options *Options) (preparedTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		names, err := r.eksClusterNames(ctx, cfg)
		if err != nil {
			return preparedTarget{}, err
		}
		if len(names) == 0 {
			return preparedTarget{}, i18n.NewError("jumphost.runner.no_eks_cluster_found")
		}
		if len(names) == 1 {
			name = names[0]
		} else {
			sort.Strings(names)
			selectedName, err := r.selectName("EKS clusters", names, "Please specify the EKS cluster name to connect to")
			if err != nil {
				return preparedTarget{}, err
			}
			if selectedName == "" {
				return preparedTarget{}, i18n.NewError("jumphost.runner.eks_cluster_name_is")
			}
			name = selectedName
		}
	}

	cluster, err := r.describeEKSCluster(ctx, cfg, name)
	if err != nil {
		return preparedTarget{}, err
	}
	clusterName, endpoint := aws.ToString(cluster.Name), aws.ToString(cluster.Endpoint)
	if endpoint == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.eks_cluster_endpoint_is")
	}
	var caData string
	if cluster.CertificateAuthority != nil {
		caData = strings.TrimSpace(aws.ToString(cluster.CertificateAuthority.Data))
	}

	localPort, err := r.reservePort(sessionLabel(resourceEKS, clusterName, cfg), options.LocalPortNumber, defaultEKSLocalPort)
	if err != nil {
		return preparedTarget{}, err
	}

	displayName := fmt.Sprintf("EKS %s (%s) -> localhost:%d", clusterName, endpoint, localPort)
	params := map[string][]string{
		"host":            {strings.TrimPrefix(endpoint, "https://")},
		"portNumber":      {"443"},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return preparedTarget{name: clusterName, display: displayName, ssmParameters: params, target: ideTarget{ClusterCA: caData}}, nil
}

// airlockExecutable is the path of the running airlock, which kubectl runs to sign in.
func airlockExecutable() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "airlock"
}

// writeKubeconfig writes the kubeconfig pointing Kubernetes clients at the tunnel of an EKS
// cluster, signing in with airlock eks-token. It writes none without a kubeconfig directory, or
// without the certificate authority kubectl needs to trust the cluster. A failure only warns.
func (r *Runner) writeKubeconfig(clusterName, endpointHost, caData string, localPort int, cfg awsConfig) (path string, remove func() string, warning string) {
	if r.kubeconfigDir == "" || caData == "" {
		return "", nil, ""
	}
	path, removeFile, err := kubeconfig.Write(r.kubeconfigDir, &kubeconfig.Cluster{
		Name:                     strings.Join([]string{"airlock", cfg.Profile, cfg.Region, clusterName}, "-"),
		Server:                   fmt.Sprintf("https://localhost:%d", localPort),
		TLSServerName:            endpointHost,
		CertificateAuthorityData: caData,
		Command:                  airlockExecutable(),
		Args:                     append([]string{"eks-token", "--cluster-name", clusterName}, cfg.Args...),
	})
	if err != nil {
		return "", nil, "no kubeconfig for this session: " + err.Error()
	}
	return path, func() string {
		if err := removeFile(); err != nil {
			return fmt.Sprintf("Warning: failed to remove kubeconfig %s: %v", path, err)
		}
		return "kubeconfig " + path + " removed"
	}, ""
}

// eksSessionInfo explains how Kubernetes clients reach the tunnel: the session kubeconfig when
// one was written, else the kubectl flags overriding the user's own context of the cluster.
func eksSessionInfo(localPort int, endpointHost, kubeconfigPath string) []string {
	override := fmt.Sprintf("  kubectl --server https://localhost:%d --tls-server-name %s get namespaces", localPort, endpointHost)
	if kubeconfigPath == "" {
		return []string{"For kubectl, select your context of this cluster and point it at the tunnel:", override}
	}
	return []string{
		"For kubectl, k9s and other Kubernetes clients, use the kubeconfig of this session (removed when it stops):",
		"  export KUBECONFIG=" + kubeconfigPath,
		"  kubectl get namespaces",
		"",
		"Or keep your own context of this cluster and point it at the tunnel:",
		override,
	}
}
