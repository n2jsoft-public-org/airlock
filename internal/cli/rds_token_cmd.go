package cli

import (
	"strconv"

	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/spf13/cobra"
)

// newRDSTokenCmd prints a temporary AWS IAM token for a database user, valid 15 minutes: the
// password of an IDE data source or a database client.
func newRDSTokenCmd() *cobra.Command {
	var host, user, profile, region string
	var port int
	cmd := &cobra.Command{
		Use:   "rds-token",
		Short: i18n.T("cli.rds_token.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := awsapi.NewConfigs().Get(cmd.Context(), profile, region)
			if err != nil {
				return tokenError(err, profile)
			}
			token, err := auth.BuildAuthToken(cmd.Context(), host+":"+strconv.Itoa(port), cfg.Region, user, mcpserve.SignInCredentials(cfg.Credentials))
			if err != nil {
				return tokenError(err, profile)
			}
			outf(cmd, "%s\n", token)
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "hostname", "", i18n.T("cli.rds_token.flag.hostname"))
	cmd.Flags().IntVar(&port, "port", 5432, i18n.T("cli.rds_token.flag.port"))
	cmd.Flags().StringVar(&user, "username", "", i18n.T("cli.rds_token.flag.username"))
	cmd.Flags().StringVar(&profile, "profile", "", i18n.T("cli.token.flag.profile"))
	cmd.Flags().StringVar(&region, "region", "", i18n.T("cli.token.flag.region"))
	cobra.CheckErr(cmd.MarkFlagRequired("hostname"))
	cobra.CheckErr(cmd.MarkFlagRequired("username"))
	return cmd
}
