package jumphost

import (
	"cmp"
	"context"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// SecretSummary is one Secrets Manager secret offered to hold the password of a database.
type SecretSummary struct {
	ARN         string
	Name        string
	Description string
	// Linked reports that the secret belongs to the instance: tagged DBInstanceArn, the RDS master
	// user secret, or named after the instance.
	Linked bool
}

// ListDBSecrets returns every secret of the profile and region of options, those linked to the
// RDS instance options.Name first, then by name.
func (r *Runner) ListDBSecrets(ctx context.Context, options *Options) ([]SecretSummary, error) {
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(options.ResourceType), resourceElastiCache) {
		return r.listSecrets(ctx, cfg, "", "", "")
	}
	instance := strings.TrimSpace(options.Name)
	described, err := r.describeDBInstance(ctx, cfg, instance)
	if err != nil {
		return nil, err
	}

	return r.listSecrets(ctx, cfg, instance, aws.ToString(described.DBInstanceArn), rdsMasterSecretARN(&described))
}

// listSecrets lists every secret of cfg, those linked to instance first (see rankDBSecrets).
func (r *Runner) listSecrets(ctx context.Context, cfg awsConfig, instance, instanceARN, masterSecret string) ([]SecretSummary, error) {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var entries []smtypes.SecretListEntry
	pages := secretsmanager.NewListSecretsPaginator(secretsmanager.NewFromConfig(awsCfg), &secretsmanager.ListSecretsInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, i18n.NewError("jumphost.secrets.failed_to_list_secrets", awsCallError(err))
		}
		entries = append(entries, page.SecretList...)
	}
	if len(entries) == 0 {
		// The identity check of resolveAWSConfig learned the account.
		where := cfg.Region
		if account := r.accountFor(ctx, cfg.Profile); account != "" {
			where = account + "/" + cfg.Region
		}
		return nil, i18n.NewError("jumphost.secrets.no_secret_in", where)
	}
	return rankDBSecrets(entries, instance, instanceARN, masterSecret), nil
}

// rankDBSecrets summarizes entries, linked to the instance first, then by name.
func rankDBSecrets(entries []smtypes.SecretListEntry, instance, instanceARN, masterSecretARN string) []SecretSummary {
	instance = strings.ToLower(strings.TrimSpace(instance))
	secrets := make([]SecretSummary, 0, len(entries))
	for idx := range entries {
		entry := &entries[idx]
		arn, name := aws.ToString(entry.ARN), aws.ToString(entry.Name)
		linked := (masterSecretARN != "" && arn == masterSecretARN) ||
			(instance != "" && strings.Contains(strings.ToLower(name), instance))
		for _, tag := range entry.Tags {
			if aws.ToString(tag.Key) == "DBInstanceArn" && instanceARN != "" && aws.ToString(tag.Value) == instanceARN {
				linked = true
			}
		}
		secrets = append(secrets, SecretSummary{ARN: arn, Name: name, Description: aws.ToString(entry.Description), Linked: linked})
	}
	sort.SliceStable(secrets, func(i, j int) bool {
		if secrets[i].Linked != secrets[j].Linked {
			return secrets[i].Linked
		}
		return secrets[i].Name < secrets[j].Name
	})
	return secrets
}

// secretShortName is the name of a secret without its path and random suffix, e.g.
// example-dev-integ.
func secretShortName(secretID string) string {
	name := SecretDisplayName(secretID)
	return name[strings.LastIndex(name, "/")+1:]
}

// SecretDisplayName returns the name of a secret ARN without its random suffix; any other secret
// ID (a name) is returned unchanged.
func SecretDisplayName(secretID string) string {
	secretID = strings.TrimSpace(secretID)
	_, name, ok := strings.Cut(secretID, ":secret:")
	if !strings.HasPrefix(secretID, "arn:") || !ok {
		return secretID
	}
	return dbmcp.SecretARNSuffix.ReplaceAllString(name, "")
}

// resolveSecretSignIn reads the password secret once before connecting, so an unreadable secret
// fails before the tunnel opens, and returns the user (the secret's, else typedUser) and the ARN to
// read it with: secretID, or the current ARN of a secret recreated under its name.
func resolveSecretSignIn(ctx context.Context, cfg *aws.Config, secretID, typedUser string) (user, arn string, err error) {
	secret, err := dbmcp.ReadPasswordSecret(ctx, cfg, secretID)
	if err != nil {
		return "", "", i18n.NewError("jumphost.cache.failed_to_read_secret", secretID, err)
	}
	arn = cmp.Or(secret.ARN, secretID)
	if secret.Username != "" {
		return secret.Username, arn, nil
	}
	if user := strings.TrimSpace(typedUser); user != "" {
		return user, arn, nil
	}
	return "", "", i18n.NewError("jumphost.secrets.secret_has_no_username", SecretDisplayName(secretID))
}

// rememberSecretARN makes options and the saved choices of their target read the secret with arn,
// when it replaces options.DBSecretARN (a secret recreated under its name). Saving is best effort:
// the stale ARN is found again by its name on the next connection.
func (r *Runner) rememberSecretARN(ctx context.Context, options *Options, arn string) {
	stale := strings.TrimSpace(options.DBSecretARN)
	if arn == stale {
		return
	}
	options.DBSecretARN = arn
	target, ok := r.prefsTarget(ctx, options)
	if !ok {
		return
	}
	if prefs, found := r.prefs.Load(target); found && prefs.DBSecretARN == stale {
		prefs.DBSecretARN = arn
		_ = r.prefs.Save(target, &prefs) //nolint:errcheck // best effort, see above
	}
}

// DBCredentials is a database sign-in read from Secrets Manager.
type DBCredentials struct {
	// Database is the database the credentials are for; empty when the secret names none.
	Database string
	// Secret is the name of the secret they come from, without its path; empty for a picked secret.
	Secret   string
	User     string
	Password string
}

// Label names the credentials for display: their database, else their secret; empty for a
// picked secret.
func (c DBCredentials) Label() string {
	if c.Database != "" {
		return c.Database
	}
	return c.Secret
}

// secretCredentialsReader returns a function reading the credentials target signs in with from
// Secrets Manager: its picked secret, else, when it signs in with neither AWS IAM nor a typed
// password, the read-only secrets of each database. It reads them again on each call, so rotated
// secrets show their current value, and never keeps them. It returns nil when neither applies.
func (r *Runner) secretCredentialsReader(cfg *aws.Config, instance string, target *ideTarget) func(ctx context.Context) ([]DBCredentials, error) {
	switch secretID, user := target.DBSecretARN, target.DBUser; {
	case secretID != "":
		return func(ctx context.Context) ([]DBCredentials, error) {
			secret, err := dbmcp.ReadPasswordSecret(ctx, cfg, secretID)
			if err != nil {
				return nil, i18n.NewError("jumphost.secrets.failed_to_read_secret", SecretDisplayName(secretID), err)
			}
			return []DBCredentials{{User: user, Password: secret.Password}}, nil
		}
	case !target.IAMAuth && target.DBPassword == "" && target.InstanceARN != "" && !r.readOnlySecretTag.IsZero():
		instanceARN, tag := target.InstanceARN, r.readOnlySecretTag
		return func(ctx context.Context) ([]DBCredentials, error) {
			secrets, err := dbmcp.ReadReadOnlySecrets(ctx, cfg, instance, instanceARN, tag)
			if err != nil {
				return nil, err
			}
			creds := make([]DBCredentials, 0, len(secrets))
			for _, secret := range secrets {
				creds = append(creds, DBCredentials{
					Database: secret.Credentials.Database,
					Secret:   secretShortName(secret.ARN),
					User:     secret.Credentials.User,
					Password: secret.Credentials.Password,
				})
			}
			sort.SliceStable(creds, func(i, j int) bool { return creds[i].Label() < creds[j].Label() })
			return creds, nil
		}
	default:
		return nil
	}
}

// MCPEngineSupported reports whether the MCP server can query the RDS engine, the only case where
// airlock itself uses the database credentials.
func MCPEngineSupported(engine string) bool {
	_, ok := dbmcp.EngineFor(engine)
	return ok
}
