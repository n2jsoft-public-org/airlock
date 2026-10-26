package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func writeLanguageConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func frenchEnv(name string) string {
	if name == "LANG" {
		return "fr_FR.UTF-8"
	}
	return ""
}

func noOSLocale() string { return "" }

func TestResolveLanguageFollowsTheSystemWhenAuto(t *testing.T) {
	if got := resolveLanguage(writeLanguageConfig(t, `{}`), frenchEnv, noOSLocale); got != "fr" {
		t.Fatalf("resolveLanguage = %q, want fr", got)
	}
}

func TestResolveLanguageConfigWins(t *testing.T) {
	path := writeLanguageConfig(t, `{"language": "en"}`)
	if got := resolveLanguage(path, frenchEnv, noOSLocale); got != "en" {
		t.Fatalf("resolveLanguage = %q, want the configured en", got)
	}
}

func TestResolveLanguageIgnoresABrokenConfig(t *testing.T) {
	path := writeLanguageConfig(t, `{"language": "en", "mcpGatewayPort": "80"}`)
	if got := resolveLanguage(path, frenchEnv, noOSLocale); got != "fr" {
		t.Fatalf("resolveLanguage = %q, want the system's fr", got)
	}
	if got := resolveLanguage("", frenchEnv, noOSLocale); got != "fr" {
		t.Fatalf("resolveLanguage without a path = %q, want fr", got)
	}
}

func rootHelp(t *testing.T, lang string) string {
	t.Helper()
	i18n.SetLanguage(lang)
	t.Cleanup(func() { i18n.SetLanguage("en") })
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestHelpStaysCobrasInEnglish(t *testing.T) {
	help := rootHelp(t, "en")
	for _, want := range []string{"Usage:", "Available Commands:", "Flags:", "help for airlock", "Help about any command"} {
		if !strings.Contains(help, want) {
			t.Errorf("English help lacks %q:\n%s", want, help)
		}
	}
}

func TestHelpInFrench(t *testing.T) {
	help := rootHelp(t, "fr")
	for _, want := range []string{"Utilisation :", "Commandes disponibles :", "Options :", "aide pour airlock", "Aide sur n'importe quelle commande"} {
		if !strings.Contains(help, want) {
			t.Errorf("French help lacks %q:\n%s", want, help)
		}
	}
	for _, unwanted := range []string{"Usage:", "Available Commands:", "help for airlock"} {
		if strings.Contains(help, unwanted) {
			t.Errorf("French help still has %q", unwanted)
		}
	}
}

// TestEveryCommandHelpIsTranslated walks the French tree: no Short and no flag usage may be left
// identical to its English text (Cobra's help, completion and flag-less commands aside).
func TestEveryCommandHelpIsTranslated(t *testing.T) {
	english := map[string]string{}
	var collect func(prefix string, c *cobra.Command, into map[string]string)
	collect = func(prefix string, c *cobra.Command, into map[string]string) {
		path := prefix + "/" + c.Name()
		into[path] = c.Short
		c.LocalFlags().VisitAll(func(f *pflag.Flag) { into[path+" --"+f.Name] = f.Usage })
		for _, sub := range c.Commands() {
			collect(path, sub, into)
		}
	}
	i18n.SetLanguage("en")
	collect("", NewRootCmd(), english)
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	french := map[string]string{}
	collect("", NewRootCmd(), french)
	for path, text := range french {
		if text != "" && text == english[path] {
			t.Errorf("%s is not translated: %q", path, text)
		}
	}
}

func runFrench(t *testing.T, args ...string) (string, error) {
	t.Helper()
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestConfigMessagesInFrench(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // appdir reads an absolute XDG_CONFIG_HOME first.
	out, err := runFrench(t, "config", "init")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Écrit ") || !strings.Contains(out, "airlock config set <clé> <valeur>") {
		t.Fatalf("config init output is not French:\n%s", out)
	}
	_, err = runFrench(t, "config", "init")
	if err == nil || !strings.Contains(i18n.Localize(err), "existe déjà") {
		t.Fatalf("second config init error = %v, want French 'existe déjà'", err)
	}
}

func TestConfigShowInFrench(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, err := runFrench(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(aucune)", "défaut"} {
		if !strings.Contains(out, want) {
			t.Errorf("config show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(none)") {
		t.Errorf("config show still says (none):\n%s", out)
	}
}

func TestOutfLocalizesErrorArgs(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	outf(cmd, "%v\n", i18n.NewError("cli.connection_test.no_password_given"))
	if got := out.String(); got != "aucun mot de passe donné\n" {
		t.Fatalf("outf = %q, want the French error", got)
	}
}
