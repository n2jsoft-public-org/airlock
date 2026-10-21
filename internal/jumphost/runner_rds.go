package jumphost

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/i18n"
)

func (r *Runner) prepareRDS(ctx context.Context, cfg awsConfig, options *Options) (preparedTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		instances, err := r.rdsInstances(ctx, cfg)
		if err != nil {
			return preparedTarget{}, err
		}
		if len(instances) == 0 {
			return preparedTarget{}, i18n.NewError("jumphost.runner.no_rds_instance_found")
		}
		if len(instances) == 1 {
			name = aws.ToString(instances[0].DBInstanceIdentifier)
		} else {
			names := make([]string, 0, len(instances))
			for idx := range instances {
				names = append(names, aws.ToString(instances[idx].DBInstanceIdentifier))
			}
			sort.Strings(names)
			selectedName, err := r.selectName("RDS instances", names, "Please specify the RDS instance name to connect to")
			if err != nil {
				return preparedTarget{}, err
			}
			if selectedName == "" {
				return preparedTarget{}, i18n.NewError("jumphost.runner.rds_instance_name_is")
			}
			name = selectedName
		}
	}

	instance, err := r.describeDBInstance(ctx, cfg, name)
	if err != nil {
		return preparedTarget{}, err
	}
	rdsHost, rdsPort := rdsEndpoint(&instance)
	if rdsHost == "" || rdsPort == 0 {
		return preparedTarget{}, i18n.NewError("jumphost.runner.rds_instance_endpoint_is")
	}
	engine, instanceID, instanceARN := aws.ToString(instance.Engine), aws.ToString(instance.DBInstanceIdentifier), aws.ToString(instance.DBInstanceArn)

	secretID := strings.TrimSpace(options.DBSecretARN)
	if secretID == "" && options.DBPassword != "" && strings.TrimSpace(options.DBUser) == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.database_user_is_required")
	}
	iamAuth := aws.ToBool(instance.IAMDatabaseAuthenticationEnabled) && options.DBPassword == "" && secretID == "" && options.SignIn != SignInAuto
	if iamAuth && r.ResolveDBUser(engine, options.DBUser) == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.database_user_is_required_2", instanceID)
	}
	var secretUser string
	if secretID != "" {
		awsCfg, cfgErr := r.sdkConfig(ctx, cfg)
		if cfgErr != nil {
			return preparedTarget{}, cfgErr
		}
		if secretUser, secretID, err = resolveSecretSignIn(ctx, &awsCfg, secretID, options.DBUser); err != nil {
			return preparedTarget{}, err
		}
		r.rememberSecretARN(ctx, options, secretID)
	}

	localPort, err := r.reservePort(sessionLabel(resourceRDS, instanceID, cfg), options.LocalPortNumber, rdsPort)
	if err != nil {
		return preparedTarget{}, err
	}

	displayName := fmt.Sprintf(
		"RDS %s %s:%d -> localhost:%d",
		engine,
		instanceID,
		rdsPort,
		localPort,
	)

	awsArgsHint := strings.Join(cfg.Args, " ")
	var sessionInfo []string
	var dbUser string
	switch {
	case secretID != "":
		dbUser = secretUser
		sessionInfo = rdsSecretSessionInfo(localPort, dbUser, secretID, awsArgsHint)
	case options.DBPassword != "":
		dbUser = strings.TrimSpace(options.DBUser)
		sessionInfo = rdsPasswordSessionInfo(localPort, dbUser)
	case iamAuth:
		dbUser = r.ResolveDBUser(engine, options.DBUser)
		sessionInfo = rdsIAMSessionInfo(localPort, rdsHost, rdsPort, dbUser, cfg, awsArgsHint)
	default:
		sessionInfo = rdsSecretsSessionInfo(localPort, instanceARN, rdsMasterSecretARN(&instance), aws.ToString(instance.MasterUsername), r.readOnlySecretTag, awsArgsHint)
	}

	params := map[string][]string{
		"host":            {rdsHost},
		"portNumber":      {fmt.Sprintf("%d", rdsPort)},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	password := options.DBPassword
	if secretID != "" {
		password = ""
	}

	return preparedTarget{name: instanceID, display: displayName, info: sessionInfo, ssmParameters: params, target: ideTarget{
		Engine:          engine,
		Database:        aws.ToString(instance.DBName),
		RDSHost:         rdsHost,
		RDSPort:         rdsPort,
		InstanceARN:     instanceARN,
		MasterSecretARN: rdsMasterSecretARN(&instance),
		IAMAuth:         iamAuth,
		DBUser:          dbUser,
		DBPassword:      password,
		DBSecretARN:     secretID,
	}}, nil
}

// rdsSecretSessionInfo explains how to sign in with the password of the picked secret.
func rdsSecretSessionInfo(localPort int, user, secretID, awsArgsHint string) []string {
	return []string{
		"Signs in with the password of a Secrets Manager secret:",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = " + user,
		"  password             = read it with:",
		fmt.Sprintf("  aws secretsmanager get-secret-value --secret-id %s --query SecretString --output text %s", secretID, awsArgsHint),
	}
}

// rdsIAMSessionInfo explains how to sign in with AWS IAM authentication.
func rdsIAMSessionInfo(localPort int, rdsHost string, rdsPort int, iamUser string, cfg awsConfig, awsArgsHint string) []string {
	return []string{
		"For tools which support AWS IAM authentication, use:",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = " + iamUser,
		fmt.Sprintf("  region               = %s", cfg.Region),
		fmt.Sprintf("  credential/profile   = %s", cfg.Profile),
		fmt.Sprintf("  rds host:port        = %s:%d", rdsHost, rdsPort),
		"",
		"Otherwise, generate a temporary token (valid for 15 minutes) with:",
		fmt.Sprintf("  airlock rds-token --hostname %s --port %d --username %s %s", rdsHost, rdsPort, iamUser, awsArgsHint),
	}
}

// rdsPasswordSessionInfo explains how to sign in with the password entered in place of AWS IAM.
func rdsPasswordSessionInfo(localPort int, user string) []string {
	return []string{
		"Signs in with a password (AWS IAM passwordless sign-in turned off):",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = " + user,
		"  password             = the one entered before connecting (kept in memory only; the IDE asks for it)",
	}
}

// rdsSecretsSessionInfo explains where the credentials of an instance without IAM authentication (SQL Server) live.
func rdsSecretsSessionInfo(localPort int, instanceARN, masterSecretARN, masterUsername string, readOnlyTag awstag.Tag, awsArgsHint string) []string {
	info := []string{
		"IAM authentication is not enabled for this instance: use Secrets Manager.",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user / password      = read them from a secret below (the JSON holds both)",
	}
	if !readOnlyTag.IsZero() {
		info = append(info,
			"",
			"Read-only credentials, one secret per database: the secrets tagged both",
			fmt.Sprintf("  %s=%s and DBInstanceArn=%s", readOnlyTag.Key, readOnlyTag.Value, instanceARN),
			"  aws secretsmanager get-secret-value --secret-id <arn> --query SecretString --output text "+awsArgsHint,
		)
	}
	if masterSecretARN == "" {
		return append(info, "", "No master user secret found for this instance.")
	}
	return append(info,
		"",
		fmt.Sprintf("Master credentials (%s), administrators only - avoid unless strictly necessary:", masterUsername),
		fmt.Sprintf("  aws secretsmanager get-secret-value --secret-id %q --query SecretString --output text %s", masterSecretARN, awsArgsHint),
	)
}

// ResolveDBUser returns the database user of an RDS instance of engine (an AWS RDS engine name):
// user, else the configured default user of the engine; "" when neither is set.
func (r *Runner) ResolveDBUser(engine, user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return r.defaultDBUser.For(engine)
}
