package mcpserve

// IsolateLocks keeps the client configuration locks in dir until restore runs, so tests of
// packages registering servers never touch the user's cache directory. Not safe for parallel tests.
func IsolateLocks(dir string) (restore func()) {
	previous := lockRoot
	lockRoot = func() (string, error) { return dir, nil }
	return func() { lockRoot = previous }
}

// IsolateUserConfigDir puts the user configuration directory, where Claude Desktop and VS Code keep
// their servers, in dir until restore runs, so tests of packages registering user-level servers
// never touch the user's apps. Not safe for parallel tests.
func IsolateUserConfigDir(dir string) (restore func()) {
	previous := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	return func() { userConfigDir = previous }
}
