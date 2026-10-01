// Package uninstall removes airlock from a machine: its binary and the PATH entry its installers
// added and, on request, the files and keychain secrets it keeps for the user.
package uninstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/n2jsoft/airlock/internal/keychain"
)

// Kind tells what an Item is.
type Kind int

const (
	// Binary is the running airlock executable.
	Binary Kind = iota
	// InstallDir is the folder install.ps1 created for the binary (Windows), once it is empty.
	InstallDir
	// ProfileEntry is the PATH block install.sh wrote to a shell profile.
	ProfileEntry
	// UserPathEntry is the install folder in the Windows user PATH.
	UserPathEntry
	// UserData is the folder of settings, recent connections, MCP query logs and kubeconfigs.
	UserData
	// KeychainSecrets are the database passwords saved in the OS keychain.
	KeychainSecrets
)

// Item is one thing an uninstall removes.
type Item struct {
	// Path is the file or folder concerned; empty for KeychainSecrets.
	Path string
	Kind Kind
}

// Describe tells what removing the item does, for the list shown before confirming.
func (i Item) Describe() string {
	switch i.Kind {
	case Binary:
		return i.Path + " (the airlock binary)"
	case InstallDir:
		return i.Path + " (its folder, once empty)"
	case ProfileEntry:
		return "the PATH entry in " + i.Path + " (added by the airlock installer)"
	case UserPathEntry:
		return i.Path + " from your user PATH"
	case UserData:
		return i.Path + " (settings, recent connections, MCP query logs and kubeconfigs)"
	case KeychainSecrets:
		return "the database passwords saved in the OS keychain"
	default:
		return i.Path
	}
}

// Env is the machine an uninstall works on: the cli fills it in, tests point it at a temp dir.
type Env struct {
	// Keychain is the OS keychain; nil when it does not answer.
	Keychain keychain.Store
	// UserPath reads the Windows user PATH; nil elsewhere.
	UserPath func() (string, error)
	GOOS     string
	// Executable is the running airlock, symlinks resolved.
	Executable string
	// DataDir is airlock's config folder.
	DataDir string
	// Profiles are the shell profiles install.sh may have written to (ShellProfiles).
	Profiles []string
}

// Plan lists what an uninstall removes, in the order shown, and what it leaves behind.
type Plan struct {
	Remove []Item
	// Kept explains what stays, one sentence each.
	Kept []string
}

// NewPlan lists what uninstalling airlock removes; purge adds the user's data. The PATH entry goes
// only when the install folder holds nothing but airlock, since other programs may need it.
func NewPlan(env *Env, purge bool) (Plan, error) {
	plan := Plan{Remove: []Item{{Kind: Binary, Path: env.Executable}}}
	dir := filepath.Dir(env.Executable)
	alone, err := onlyAirlockIn(dir, filepath.Base(env.Executable))
	if err != nil {
		return Plan{}, err
	}
	pathItems, err := pathEntries(env, dir)
	if err != nil {
		return Plan{}, err
	}
	switch {
	case alone:
		if env.GOOS == "windows" {
			plan.Remove = append(plan.Remove, Item{Kind: InstallDir, Path: dir})
		}
		plan.Remove = append(plan.Remove, pathItems...)
	case len(pathItems) > 0:
		plan.Kept = append(plan.Kept, "the PATH entry of "+dir+", since other programs are in it")
	}

	hasData := exists(env.DataDir)
	switch {
	case purge && hasData:
		plan.Remove = append(plan.Remove, Item{Kind: UserData, Path: env.DataDir})
	case hasData:
		plan.Kept = append(plan.Kept, env.DataDir+" (settings, recent connections, MCP query logs): add --purge to remove it")
	}
	switch {
	case purge && env.Keychain != nil:
		plan.Remove = append(plan.Remove, Item{Kind: KeychainSecrets})
	case env.Keychain != nil:
		plan.Kept = append(plan.Kept, "the database passwords saved in the OS keychain: add --purge to remove them")
	}
	plan.Kept = append(plan.Kept, "the IntelliJ data sources airlock added to your projects' .idea/dataSources.xml")
	return plan, nil
}

// pathEntries returns the PATH entries the installers added for dir.
func pathEntries(env *Env, dir string) ([]Item, error) {
	if env.GOOS == "windows" {
		if env.UserPath == nil {
			return nil, nil
		}
		list, err := env.UserPath()
		if err != nil {
			return nil, fmt.Errorf("cannot read your user PATH: %w", err)
		}
		if _, found := removePathEntry(list, dir); found {
			return []Item{{Kind: UserPathEntry, Path: dir}}, nil
		}
		return nil, nil
	}
	var items []Item
	for _, profile := range env.Profiles {
		content, err := os.ReadFile(profile) // #nosec G304 -- a shell profile of the user.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", profile, err)
		}
		if _, found := removeProfileBlock(string(content), dir); found {
			items = append(items, Item{Kind: ProfileEntry, Path: profile})
		}
	}
	return items, nil
}

// onlyAirlockIn tells whether dir holds nothing but the airlock binary called name, and what
// updates and installers leave next to it (name.old*, name.new, .airlock.new).
func onlyAirlockIn(dir, name string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("cannot read %s: %w", dir, err)
	}
	for _, entry := range entries {
		n := entry.Name()
		if n != name && !strings.HasPrefix(n, name+".old") && n != name+".new" && n != ".airlock.new" {
			return false, nil
		}
	}
	return true, nil
}

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Apply removes every item of plan and calls done after each one. It goes on after a failure and
// returns every error. The binary goes last: on Windows it is deleted once airlock exits.
func Apply(plan *Plan, env *Env, done func(Item)) error {
	var errs []error
	var binary *Item
	removeDir := false
	for i := range plan.Remove {
		item := plan.Remove[i]
		var err error
		switch item.Kind {
		case Binary:
			binary = &plan.Remove[i]
			continue
		case InstallDir:
			removeDir = true
			continue
		case ProfileEntry:
			err = removeFromProfile(item.Path, filepath.Dir(env.Executable))
		case UserPathEntry:
			err = removeFromUserPath(item.Path)
		case UserData:
			err = os.RemoveAll(item.Path)
		case KeychainSecrets:
			err = env.Keychain.DeleteAll()
			if err != nil {
				err = fmt.Errorf("cannot remove the passwords saved in the OS keychain: %w", err)
			}
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		done(item)
	}
	if binary != nil {
		if err := removeBinary(binary.Path, removeDir); err != nil {
			errs = append(errs, err)
		} else {
			done(*binary)
		}
	}
	return errors.Join(errs...)
}

// removeFromProfile drops the installer's PATH block for dir from profile; the fish file, which
// only holds that block, goes entirely.
func removeFromProfile(profile, dir string) error {
	content, err := os.ReadFile(profile) // #nosec G304 -- a shell profile of the user.
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", profile, err)
	}
	updated, found := removeProfileBlock(string(content), dir)
	if !found {
		return nil
	}
	if updated == "" && filepath.Base(profile) == "airlock.fish" {
		if err = os.Remove(profile); err != nil {
			return fmt.Errorf("cannot remove %s: %w", profile, err)
		}
		return nil
	}
	info, err := os.Stat(profile)
	if err != nil {
		return fmt.Errorf("cannot update %s: %w", profile, err)
	}
	if err = os.WriteFile(profile, []byte(updated), info.Mode().Perm()); err != nil { // #nosec G703 -- a shell profile of the user.
		return fmt.Errorf("cannot update %s: %w", profile, err)
	}
	return nil
}
