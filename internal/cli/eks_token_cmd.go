package cli

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/kubeconfig"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/spf13/cobra"
)

// tokenError words a failure of a token command: missing or expired credentials as airlock's
// sign-in error, anything else as the AWS API reports it.
func tokenError(err error, profile string) error {
	if _, ok := errors.AsType[*mcpserve.CredentialsError](err); ok {
		return i18n.NewError("jumphost.identity.aws_credentials_of_profile", jumphost.ErrSignInRequired, profile)
	}
	return mcpserve.AWSError(err)
}

// newEKSTokenCmd prints the ExecCredential kubectl runs, through the kubeconfig airlock writes,
// to sign in to an EKS cluster.
func newEKSTokenCmd() *cobra.Command {
	var cluster, profile, region string
	cmd := &cobra.Command{
		Use:   "eks-token",
		Short: i18n.T("cli.eks_token.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := awsapi.NewConfigs().Get(cmd.Context(), profile, region)
			if err != nil {
				return tokenError(err, profile)
			}
			cfg.Credentials = mcpserve.SignInCredentials(cfg.Credentials)
			token, expires, err := awsapi.EKSToken(cmd.Context(), &cfg, cluster)
			if err != nil {
				return tokenError(err, profile)
			}
			credential := map[string]any{
				"apiVersion": kubeconfig.ExecAPIVersion,
				"kind":       "ExecCredential",
				"status":     map[string]string{"token": token, "expirationTimestamp": expires.UTC().Format(time.RFC3339)},
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(credential)
		},
	}
	cmd.Flags().StringVar(&cluster, "cluster-name", "", i18n.T("cli.eks_token.flag.cluster-name"))
	cmd.Flags().StringVar(&profile, "profile", "", i18n.T("cli.token.flag.profile"))
	cmd.Flags().StringVar(&region, "region", "", i18n.T("cli.token.flag.region"))
	cobra.CheckErr(cmd.MarkFlagRequired("cluster-name"))
	return cmd
}
