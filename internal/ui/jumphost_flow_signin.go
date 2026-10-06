package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
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
	if m.options.ResourceType == "elasticache" {
		return m.cacheSignInOptions()
	}
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

// cacheSignInOptions lists the sign-in methods of a cache, the default first: AWS IAM only with an
// RBAC user group; none without auth, or when no MCP server uses the credentials.
func (m jumphostWizardModel) cacheSignInOptions() []string {
	if !m.mcpApplies() {
		return nil
	}
	switch m.targetCacheAuth {
	case jumphost.CacheAuthRBAC:
		return []string{jumphost.SignInIAM, jumphost.SignInPassword, jumphost.SignInSecret}
	case jumphost.CacheAuthToken:
		return []string{jumphost.SignInPassword, jumphost.SignInSecret}
	default:
		return nil
	}
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
		m.prefilledFor = ""
	}
	return m
}

// dropPrefilledPasswordOfOtherUser empties the password field filled from the keychain once the
// User field names another user: that password is not theirs, and saving it would move it to
// their keychain entry. Remember is left as the user set it.
func (m jumphostWizardModel) dropPrefilledPasswordOfOtherUser() jumphostWizardModel {
	if m.prefilledFor != "" && strings.TrimSpace(m.userInput.Value()) != m.prefilledFor {
		m.passwordInput.Reset()
		m.prefilledFor = ""
	}
	return m
}

// applySavedSignIn starts the review from the target's saved method when it is still offered,
// else from the first one; a remembered password is prefilled.
func (m jumphostWizardModel) applySavedSignIn(msg jumphostDefaultPortLoadedMsg) jumphostWizardModel {
	options := m.signInOptions()
	m.remember, m.secret, m.prefilledFor = false, jumphost.SecretSummary{}, ""
	if len(options) == 0 {
		m.signIn = ""
		return m
	}
	m.signIn = options[0]
	// A --db-secret flag wins over the saved choices, as --db-user wins over the saved user.
	if flag := strings.TrimSpace(m.base.DBSecretARN); flag != "" && slices.Contains(options, jumphost.SignInSecret) {
		m.signIn = jumphost.SignInSecret
		m.secret = jumphost.SecretSummary{ARN: flag, Name: jumphost.SecretDisplayName(flag)}
		return m
	}
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
			m.prefilledFor = strings.TrimSpace(m.userInput.Value())
		}
	case jumphost.SignInSecret:
		if arn := msg.prefs.DBSecretARN; arn != "" {
			m.secret = jumphost.SecretSummary{ARN: arn, Name: jumphost.SecretDisplayName(arn)}
		}
	}
	return m
}

// applySignIn validates the sign-in fields and copies them to the options; on error it returns
// the field to focus and the message. The method is only recorded when the user could choose a
// password or a secret: otherwise options.SignIn stays empty, so the choices saved for the target
// (a remembered password, a secret) outlive a connection that could not offer them (--no-mcp).
func (m jumphostWizardModel) applySignIn() (next jumphostWizardModel, field int, errMsg string) {
	m.options.SignIn = ""
	if offered := m.signInOptions(); slices.Contains(offered, jumphost.SignInPassword) || slices.Contains(offered, jumphost.SignInSecret) {
		m.options.SignIn = m.signIn
	}
	m.options.DBPassword, m.options.DBSecretARN, m.options.RememberPassword = "", "", false
	user := strings.TrimSpace(m.userInput.Value())
	if strings.ContainsAny(user, " \t") {
		return m, reviewFieldUser, "Database user must not contain spaces"
	}
	switch m.signIn {
	case jumphost.SignInIAM:
		resolved := user
		if m.options.ResourceType == "rds" {
			resolved = m.runner.ResolveDBUser(m.targetEngine, user)
		}
		if resolved == "" {
			if m.options.ResourceType == "elasticache" {
				return m, reviewFieldUser, "Enter the RBAC user signing in with AWS IAM"
			}
			return m, reviewFieldUser, "Enter the database user signing in with AWS IAM"
		}
		m.options.DBUser = resolved
	case jumphost.SignInPassword:
		if user == "" && m.options.ResourceType != "elasticache" {
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

// openSecretPicker opens the secret picker and lists the secrets of the target's account and region.
func (m jumphostWizardModel) openSecretPicker() (tea.Model, tea.Cmd) {
	m.secretPicker = newSecretPicker(m.currentRegion())
	m.pickingSecret = true
	m.errorMsg = ""
	opts := m.prefsOptions()
	return m, tea.Batch(textinput.Blink, func() tea.Msg {
		secrets, err := m.runner.ListDBSecrets(m.ctx, &opts)
		return jumphostSecretsLoadedMsg{secrets: secrets, err: err}
	})
}

// updateSecretPicker forwards msg to the open picker and applies its outcome.
func (m jumphostWizardModel) updateSecretPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	picker, outcome, cmd := m.secretPicker.Update(msg)
	m.secretPicker = picker
	switch outcome {
	case secretPickerCanceled:
		m.pickingSecret = false
		return m.focusReviewField(reviewFieldSecret), nil
	case secretPickerChosen:
		m.pickingSecret = false
		m.secret, _ = picker.choice()
		return m.focusReviewField(reviewFieldUser), nil
	default:
		// secretPickerOpen: still picking.
	}
	return m, cmd
}
