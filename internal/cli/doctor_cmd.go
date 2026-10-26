package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/appdir"
	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/daemon"
	"github.com/n2jsoft-public-org/airlock/internal/doctor"
	"github.com/n2jsoft-public-org/airlock/internal/gateway"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/keychain"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/orgconfig"
	"github.com/n2jsoft-public-org/airlock/internal/selfupdate"
	"github.com/n2jsoft-public-org/airlock/internal/ui"
	"github.com/n2jsoft-public-org/airlock/internal/version"
	"github.com/spf13/cobra"
)

// doctorProbeTimeout bounds each network read of the checks outside AWS (GitHub, organization).
const doctorProbeTimeout = 5 * time.Second

// tunnelModules are the modules whose targets need the AWS CLI and the Session Manager plugin.
var tunnelModules = []string{"sql", "k8s", "cache"}

// newDoctorCmd checks the installation, the config, the AWS profiles and the daemon, read-only.
func newDoctorCmd() *cobra.Command {
	var asJSON bool
	var profile, region string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: i18n.T("cli.doctor.short"),
		Long:  i18n.T("cli.doctor.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sections := doctorSections(cmd.Context(), profile, region)
			var results []doctor.Result
			if asJSON {
				results = ui.CheckDoctor(sections)
				data, err := json.MarshalIndent(results, "", "  ")
				if err != nil {
					return err
				}
				outf(cmd, "%s\n", string(data))
			} else {
				var err error
				if results, err = ui.ShowDoctor(sections, cmd.InOrStdin(), cmd.OutOrStdout()); errors.Is(err, ui.ErrDoctorCanceled) {
					return &ExitCodeError{Code: 130}
				} else if err != nil {
					return err
				}
			}
			if doctor.Failed(results) {
				return &ExitCodeError{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, i18n.T("cli.doctor.flag.json"))
	cmd.Flags().StringVarP(&profile, "profile", "p", "", i18n.T("cli.doctor.flag.profile"))
	cmd.Flags().StringVar(&region, "region", "", i18n.T("cli.doctor.flag.region"))
	return cmd
}

// doctorSections gathers what the checks need from config.json and the machine, and groups the
// checks in sections that run concurrently.
func doctorSections(ctx context.Context, profile, region string) []ui.DoctorSection {
	current := version.String()
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	path, pathErr := appdir.ConfigFile()
	env := orgEnvironment()
	env.cacheOnly = true // the doctor probes the source below, without refreshing the cache
	org := resolveOrganization(ctx, env, path)
	cfg, loadErr := config.Load(path, org.Org)
	if loadErr != nil {
		cfg = config.Defaults()
	}
	settings, settingsErr := config.Settings(path, org.Org)
	tunnels := slices.ContainsFunc(tunnelModules, func(m string) bool { return !slices.Contains(cfg.DisabledModules, m) })

	return []ui.DoctorSection{
		{Title: i18n.T("cli.doctor.section.install"), Run: func() []doctor.Result {
			latest, checkErr := latestRelease(ctx, current)
			return append([]doctor.Result{doctor.Version(current, latest, checkErr), doctor.OtherBinaries(self, os.Getenv("PATH"))},
				doctor.TunnelTools(ctx, tunnels)...)
		}},
		{Title: i18n.T("cli.doctor.section.config"), Run: func() []doctor.Result {
			return append([]doctor.Result{doctor.Config(path, errors.Join(pathErr, loadErr, settingsErr), deprecatedKeys(settings))},
				doctor.Organization(org.Source, organizationProbe(ctx, env, &org, settings))...)
		}},
		{Title: i18n.T("cli.doctor.section.aws"), Run: func() []doctor.Result {
			return doctorProfiles(ctx, &cfg, profile, region, tunnels)
		}},
		{Title: i18n.T("cli.doctor.section.daemon"), Run: func() []doctor.Result {
			return doctorDaemon(ctx, &cfg, current)
		}},
		{Title: i18n.T("cli.doctor.section.keychain"), Run: func() []doctor.Result {
			return []doctor.Result{doctorKeychain(newKeychain, doctorProbeTimeout)}
		}},
	}
}

// doctorKeychain checks the keychain open returns, as not responding when it has not answered
// within timeout: a locked Linux keyring may wait for an unlock forever. The check is then left
// running; the doctor exits without it.
func doctorKeychain(open func() keychain.Store, timeout time.Duration) doctor.Result {
	answer := make(chan bool, 1)
	go func() { answer <- keychain.Available(open()) }()
	select {
	case available := <-answer:
		return doctor.Keychain(available)
	case <-time.After(timeout):
		return doctor.KeychainNotResponding()
	}
}

// latestRelease is the newer release than current ("" when none), unless the update check is off
// or the build has no version. It never saves the check.
func latestRelease(ctx context.Context, current string) (string, error) {
	if os.Getenv(noUpdateCheckEnv) != "" || current == version.Unknown {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	updater, err := defaultUpdateEnv().updater(ctx)
	if err != nil {
		return "", err
	}
	result, err := updater.Check(ctx)
	if err != nil || result.Status != selfupdate.StatusAvailable {
		return "", err
	}
	return result.Latest, nil
}

// deprecatedKeys lists the deprecated keys settings still read, as "old → new" when renamed.
func deprecatedKeys(settings []config.Setting) []string {
	var keys []string
	for i := range settings {
		if settings[i].Source != config.SourceDeprecated {
			continue
		}
		key := settings[i].Name
		if newKey, renamed := config.Renamed(key); renamed {
			key += " → " + newKey
		}
		keys = append(keys, key)
	}
	return keys
}

// organizationProbe is the notices of org, with why its remote source cannot be read now.
func organizationProbe(ctx context.Context, env orgEnv, org *orgState, settings []config.Setting) []string { //nolint:gocritic // orgEnv is a value the commands build.
	notices := organizationNotices(*org, settings, env.now())
	if org.Source == "" || org.src.Raw == "" || org.src.Kind == orgconfig.KindFile {
		return notices // a file source is read by resolveOrganization, its failure is a notice
	}
	ctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	if _, err := env.fetcher.Fetch(ctx, org.src, ""); err != nil {
		notices = append(notices, organizationWarning(org.Source, err))
	}
	return notices
}

// doctorProfiles checks the AWS profiles (only profile when given) in region or the one each
// resolves; the jumphost only when tunnels are used.
func doctorProfiles(ctx context.Context, cfg *config.Config, profile, region string, tunnels bool) []doctor.Result {
	runner := jumphost.NewRunner(&jumphost.RunnerOptions{
		Out: io.Discard, Err: io.Discard, JumphostTag: cfg.JumphostTag, DefaultRegion: cfg.DefaultRegion,
	})
	names := []string{profile}
	if profile == "" {
		var err error
		if names, err = awsapi.Profiles(); err != nil {
			return []doctor.Result{{Check: "aws-profile", Status: doctor.Failure,
				Message: i18n.Format("cli.doctor.profiles_unreadable", err), Fix: i18n.T("cli.doctor.profiles_fix")}}
		}
		if len(names) == 0 {
			return []doctor.Result{{Check: "aws-profile", Status: doctor.Warning,
				Message: i18n.T("cli.doctor.profiles_none"), Fix: i18n.T("cli.doctor.profiles_fix")}}
		}
	}
	profiles := make([]doctor.Profile, 0, len(names))
	for _, name := range names {
		p := doctor.Profile{Name: name, Region: region}
		if p.Region == "" {
			p.Region = runner.ResolveRegion(ctx, name)
		}
		profiles = append(profiles, p)
	}
	return doctor.Profiles(ctx, runner, profiles, tunnels, doctor.AWSTimeout)
}

// doctorDaemon checks the daemon (airlock daemon status) and, in daemon mode with the gateway on,
// its port and its entry in the user-level AI client files.
func doctorDaemon(ctx context.Context, cfg *config.Config, current string) []doctor.Result {
	daemonMode := cfg.Mode == config.ModeDaemon
	running, daemonVersion, gatewayStatus := false, "", ""
	if paths, err := daemon.DefaultPaths(); err == nil {
		client, dialErr := daemon.Running(ctx, paths, current)
		if client != nil {
			defer func() { _ = client.Close() }() //nolint:errcheck // a status read only.
			running, daemonVersion = true, current
			if mismatch, ok := errors.AsType[*daemon.VersionMismatchError](dialErr); ok {
				daemonVersion = mismatch.Daemon
			}
			if status, statusErr := client.Status(ctx); statusErr == nil {
				gatewayStatus = status.Gateway
			}
		}
	}
	results := []doctor.Result{doctor.Daemon(daemonMode, running, daemonVersion, current)}
	if !daemonMode || !cfg.MCPGateway {
		return results
	}
	entry := mcpserve.ClientEntry{Name: mcpserve.GatewayName(cfg.MCPServerPrefix), URL: gateway.URL(cfg.MCPGatewayPort)}
	files, err := mcpserve.EntryFiles(cfg.MCPClientSelection().UserOnly(), entry)
	return append(results, doctor.GatewayPort(cfg.MCPGatewayPort, running, gatewayStatus), doctor.GatewayClients(files, err))
}
