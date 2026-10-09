package cli

import (
	"io"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/spf13/cobra"
)

// registerCompletions completes the arguments of cmd from the remembered connections, the
// regions from a built-in list and the profiles from the AWS CLI config, without any AWS API call.
func registerCompletions(cmd *cobra.Command) {
	cmd.ValidArgsFunction = completeArgs
	for _, flag := range []string{"profile", "aws-profile"} {
		cobra.CheckErr(cmd.RegisterFlagCompletionFunc(flag, completeProfiles))
	}
	cobra.CheckErr(cmd.RegisterFlagCompletionFunc("region", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return ui.RegionCompletions(), cobra.ShellCompDirectiveNoFileComp
	}))
}

// completeArgs completes the resource type, then the targets remembered for it, then the local
// port they last used.
func completeArgs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return []string{
			"rds\t" + i18n.T("cli.completion.rds"), "eks\t" + i18n.T("cli.completion.eks"), "elasticache\t" + i18n.T("cli.completion.elasticache"),
			"direct\t" + i18n.T("cli.completion.direct"), "docker\t" + i18n.T("cli.completion.docker"),
		}, cobra.ShellCompDirectiveNoFileComp
	case 1:
		if strings.EqualFold(args[0], dockerResourceType) {
			return dockerCompletions(cmd), cobra.ShellCompDirectiveNoFileComp
		}
		if strings.EqualFold(args[0], directResourceType) {
			return directDatabaseCompletions(), cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		seen := map[string]bool{}
		recent := recentConnections(cmd, args[0])
		for idx := range recent {
			if conn := &recent[idx]; !seen[conn.Ref.Name] {
				seen[conn.Ref.Name] = true
				names = append(names, conn.Ref.Name+"\t"+conn.Prefs.Profile+" · "+conn.Ref.Region)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	case 2:
		if strings.EqualFold(args[0], directResourceType) || strings.EqualFold(args[0], dockerResourceType) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		recent := recentConnections(cmd, args[0])
		for idx := range recent {
			if conn := &recent[idx]; conn.Ref.Name == args[1] && conn.Prefs.LocalPort > 0 {
				return []string{strconv.Itoa(conn.Prefs.LocalPort) + "\t" + i18n.T("cli.completion.last_used")}, cobra.ShellCompDirectiveNoFileComp
			}
		}
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// dockerCompletions lists the running database containers with their description; none without docker.
func dockerCompletions(cmd *cobra.Command) []string {
	containers := dockerContainers()
	if containers == nil {
		return nil
	}
	list, err := containers.List(cmd.Context())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(list))
	for idx := range list {
		names = append(names, list[idx].Name+"\t"+list[idx].Description())
	}
	return names
}

// recentConnections lists the remembered connections of resourceType, most recent first, limited
// to the --profile and --region given; none when saved-connections.json cannot be read.
func recentConnections(cmd *cobra.Command, resourceType string) []jumphost.SavedConnection {
	store, err := savedConnectionsStore()
	if err != nil {
		return nil
	}
	saved, err := jumphost.NewPrefsStore(store.Path()).List()
	if err != nil {
		return nil
	}
	profile, region := flagValue(cmd, "profile"), flagValue(cmd, "region")
	var matching []jumphost.SavedConnection
	for idx := range saved {
		switch conn := &saved[idx]; {
		case !strings.EqualFold(conn.Ref.Type, resourceType):
		case profile != "" && conn.Prefs.Profile != profile:
		case region != "" && conn.Ref.Region != region:
		default:
			matching = append(matching, *conn)
		}
	}
	return matching
}

// flagValue is the value of the flag name of cmd; "" when it has none.
func flagValue(cmd *cobra.Command, name string) string {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	return ""
}

// completeProfiles completes the AWS CLI profiles.
func completeProfiles(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	runner := jumphost.NewRunner(&jumphost.RunnerOptions{Out: io.Discard, Err: io.Discard})
	profiles, err := runner.ListAWSProfileNames(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return profiles, cobra.ShellCompDirectiveNoFileComp
}

// directDatabaseCompletions lists the direct databases with their engine and address; none when
// saved-connections.json cannot be read.
func directDatabaseCompletions() []string {
	store, err := savedConnectionsStore()
	if err != nil {
		return nil
	}
	dbs, err := store.Databases().List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(dbs))
	for idx := range dbs {
		db := &dbs[idx]
		names = append(names, db.Name+"\t"+string(db.Engine)+" · "+db.Address())
	}
	return names
}

// completeDirectDatabase completes the only argument of a command with a direct database name.
func completeDirectDatabase(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return directDatabaseCompletions(), cobra.ShellCompDirectiveNoFileComp
}

// completeDirectDatabases completes every argument with a direct database name not given yet.
func completeDirectDatabases(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	given := make(map[string]bool, len(args))
	for _, arg := range args {
		given[arg] = true
	}
	var names []string
	for _, candidate := range directDatabaseCompletions() {
		if name, _, _ := strings.Cut(candidate, "\t"); !given[name] {
			names = append(names, candidate)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeSavedConnection completes the only argument with the saved connections: the direct
// names and the <type>/<target> of the jumphost entries, their keys when several share one.
func completeSavedConnection(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store, err := savedConnectionsStore()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	shared := map[string]int{}
	for idx := range entries {
		shared[entries[idx].ShortName()]++
	}
	names := make([]string, 0, len(entries))
	for idx := range entries {
		entry := &entries[idx]
		name := entry.ShortName()
		if shared[name] > 1 {
			name = entry.Key
		}
		names = append(names, name+"\t"+describeConnection(entry))
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
