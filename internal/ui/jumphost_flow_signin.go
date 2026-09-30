package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// signInLabels names the sign-in methods in the selector.
var signInLabels = map[string]string{
	jumphost.SignInIAM:      "IAM token",
	jumphost.SignInPassword: "Password",
	jumphost.SignInSecret:   "Secrets Manager",
	jumphost.SignInAuto:     "Auto",
}

// targetSQLServer reports whether the selected RDS target runs SQL Server.
func (m jumphostWizardModel) targetSQLServer() bool {
	return strings.HasPrefix(strings.ToLower(m.targetEngine), "sqlserver")
}

// signInOptions lists the sign-in methods of the target, the default first: AWS IAM only when the
// instance accepts it; the other methods only where airlock uses the credentials, that is when an
// MCP server is started for an engine it supports, the automatic secrets only on SQL Server.
func (m jumphostWizardModel) signInOptions() []string {
	var methods []string
	if m.targetIAMAuth {
		methods = append(methods, jumphost.SignInIAM)
	}
	if m.mcpApplies() && jumphost.MCPEngineSupported(m.targetEngine) {
		if m.targetSQLServer() {
			methods = append(methods, jumphost.SignInAuto)
		}
		methods = append(methods, jumphost.SignInPassword, jumphost.SignInSecret)
	}
	return methods
}

// signInFields are the review fields of the chosen method, after the selector.
func (m jumphostWizardModel) signInFields() []int {
	switch m.signIn {
	case jumphost.SignInIAM:
		return []int{reviewFieldUser}
	case jumphost.SignInPassword:
		if m.keychainAvailable {
			return []int{reviewFieldUser, reviewFieldPassword, reviewFieldRemember}
		}
		return []int{reviewFieldUser, reviewFieldPassword}
	case jumphost.SignInSecret:
		return []int{reviewFieldSecret, reviewFieldUser}
	default:
		return nil
	}
}

// cycleSignIn moves the method delta positions in signInOptions, wrapping around. A typed
// password is dropped when leaving the password method.
func (m jumphostWizardModel) cycleSignIn(delta int) jumphostWizardModel {
	options := m.signInOptions()
	if len(options) == 0 {
		m.signIn = ""
		return m
	}
	current := 0
	for idx, method := range options {
		if method == m.signIn {
			current = idx
		}
	}
	m.signIn = options[((current+delta)%len(options)+len(options))%len(options)]
	if m.signIn != jumphost.SignInPassword {
		m.passwordInput.Reset()
		m.remember = false
	}
	return m
}

// applySavedSignIn starts the review from the target's saved method when it is still offered,
// else from the first one; a remembered password is prefilled.
func (m jumphostWizardModel) applySavedSignIn(msg jumphostDefaultPortLoadedMsg) jumphostWizardModel {
	options := m.signInOptions()
	m.remember, m.secret = false, jumphost.SecretSummary{}
	if len(options) == 0 {
		m.signIn = ""
		return m
	}
	m.signIn = options[0]
	if !msg.saved {
		return m
	}
	saved := msg.prefs.SignInMethod()
	for _, method := range options {
		if method == saved {
			m.signIn = saved
		}
	}
	switch m.signIn {
	case jumphost.SignInPassword:
		m.remember = msg.prefs.PasswordInKeychain && m.keychainAvailable
		if msg.keychainPassword != "" {
			m.passwordInput.SetValue(msg.keychainPassword)
		}
	case jumphost.SignInSecret:
		if arn := msg.prefs.DBSecretARN; arn != "" {
			m.secret = jumphost.SecretSummary{ARN: arn, Name: jumphost.SecretDisplayName(arn)}
		}
	}
	return m
}

// applySignIn validates the sign-in fields and copies them to the options; on error it returns
// the field to focus and the message.
func (m jumphostWizardModel) applySignIn() (next jumphostWizardModel, field int, errMsg string) {
	m.options.SignIn = m.signIn
	m.options.DBPassword, m.options.DBSecretARN, m.options.RememberPassword = "", "", false
	user := strings.TrimSpace(m.userInput.Value())
	if strings.ContainsAny(user, " \t") {
		return m, reviewFieldUser, "Database user must not contain spaces"
	}
	switch m.signIn {
	case jumphost.SignInIAM:
		resolved := m.runner.ResolveDBUser(user)
		if resolved == "" {
			return m, reviewFieldUser, "Enter the database user signing in with AWS IAM"
		}
		m.options.DBUser = resolved
	case jumphost.SignInPassword:
		if user == "" {
			return m, reviewFieldUser, "Enter the database user signing in with the password"
		}
		if m.passwordInput.Value() == "" {
			return m, reviewFieldPassword, "Enter the database password, or pick another sign-in method"
		}
		m.options.DBUser = user
		m.options.DBPassword = m.passwordInput.Value()
		m.options.RememberPassword = m.remember && m.keychainAvailable
	case jumphost.SignInSecret:
		if m.secret.ARN == "" {
			return m, reviewFieldSecret, "Pick the secret holding the password (enter on Secret)"
		}
		m.options.DBUser = user
		m.options.DBSecretARN = m.secret.ARN
	default:
		m.options.DBUser = ""
	}
	return m, 0, ""
}

// openSecretPicker opens the secret picker; the next task implements it.
func (m jumphostWizardModel) openSecretPicker() (tea.Model, tea.Cmd) {
	return m, nil
}
