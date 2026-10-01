//go:build windows

package uninstall

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// UserPath reads the user PATH, as install.ps1 extends it.
var UserPath = readUserPath

func readUserPath() (string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close() //nolint:errcheck // read-only key.
	value, _, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return value, err
}

// removeFromUserPath drops dir from the user PATH, keeping the value's registry type so entries
// such as %USERPROFILE%\bin still expand, then tells Windows the environment changed.
func removeFromUserPath(dir string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("cannot update your user PATH: %w", err)
	}
	defer key.Close() //nolint:errcheck // the value is written before.
	value, valueType, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read your user PATH: %w", err)
	}
	updated, found := removePathEntry(value, dir)
	if !found {
		return nil
	}
	if valueType == registry.EXPAND_SZ {
		err = key.SetExpandStringValue("Path", updated)
	} else {
		err = key.SetStringValue("Path", updated)
	}
	if err != nil {
		return fmt.Errorf("cannot update your user PATH: %w", err)
	}
	broadcastEnvironmentChange()
	return nil
}

// broadcastEnvironmentChange lets Explorer reload the environment, so terminals opened from now on
// get the new PATH. Best effort: a new session gets it anyway.
func broadcastEnvironmentChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
		timeoutMillis   = 5000
	)
	area, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	send := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	_, _, _ = send.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(area)), //nolint:errcheck,gosec // best effort, see above.
		smtoAbortIfHung, timeoutMillis, uintptr(unsafe.Pointer(&result)))
}

// deleteAfterExit waits for airlock to exit, deletes the binary and what updates left next to it,
// then the folder when AIRLOCK_UNINSTALL_DIR names it and it is empty. Paths come through the
// environment, so none needs quoting. Nobody sees its errors, unless AIRLOCK_UNINSTALL_LOG names a
// file to record them in.
const deleteAfterExit = `$ErrorActionPreference = 'Continue'
if ($env:AIRLOCK_UNINSTALL_LOG) { Start-Transcript -LiteralPath $env:AIRLOCK_UNINSTALL_LOG | Out-Null }
Wait-Process -Id $env:AIRLOCK_UNINSTALL_PID -Timeout 60
$exe = $env:AIRLOCK_UNINSTALL_EXE
for ($i = 0; $i -lt 20 -and (Test-Path -LiteralPath $exe); $i++) {
    Remove-Item -LiteralPath $exe -Force
    if (Test-Path -LiteralPath $exe) { Start-Sleep -Milliseconds 500 }
}
Get-ChildItem -Force -LiteralPath (Split-Path $exe) |
    Where-Object { $_.Name -like ((Split-Path -Leaf $exe) + '.old*') -or $_.Name -eq ((Split-Path -Leaf $exe) + '.new') } |
    Remove-Item -Force
$dir = $env:AIRLOCK_UNINSTALL_DIR
if ($dir -and -not (Get-ChildItem -Force -LiteralPath $dir)) { Remove-Item -LiteralPath $dir -Force }
`

// removeBinary hands path to a hidden PowerShell that deletes it once airlock exits: Windows
// cannot delete a running executable. removeDir also removes its folder once empty.
func removeBinary(path string, removeDir bool) error {
	dir := ""
	if removeDir {
		dir = filepath.Dir(path)
	}
	return deleteAfterExitOf(os.Getpid(), path, dir)
}

// deleteAfterExitOf starts the hidden PowerShell that deletes path, then dir once empty, after the
// process pid exits.
func deleteAfterExitOf(pid int, path, dir string) error {
	cmd := deleteCommand(pid, path, dir)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot remove %s: %w", path, err)
	}
	return cmd.Process.Release()
}

// deleteCommand is the hidden PowerShell of deleteAfterExitOf, not started.
func deleteCommand(pid int, path, dir string) *exec.Cmd {
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", // #nosec G204 G702 -- fixed script, paths go through the environment.
		"-WindowStyle", "Hidden", "-EncodedCommand", encodeCommand(deleteAfterExit))
	cmd.Env = append(os.Environ(),
		"AIRLOCK_UNINSTALL_PID="+strconv.Itoa(pid),
		"AIRLOCK_UNINSTALL_EXE="+path,
		"AIRLOCK_UNINSTALL_DIR="+dir,
	)
	// A console of its own, without a window: started with no console at all (DETACHED_PROCESS),
	// powershell.exe exits at once without running the script.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	return cmd
}

// encodeCommand encodes a script for powershell -EncodedCommand: base64 of its UTF-16LE text.
func encodeCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, 2*len(units))
	for _, unit := range units {
		raw = binary.LittleEndian.AppendUint16(raw, unit)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// OtherInstances counts the other running processes of exe: while one runs, its binary cannot be
// deleted.
func OtherInstances(exe string) (int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("cannot list the running programs: %w", err)
	}
	defer windows.CloseHandle(snapshot) //nolint:errcheck // nothing to do about it.

	self := windows.GetCurrentProcessId()
	count := 0
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == self || !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(exe)) {
			continue
		}
		if path, pathErr := processPath(entry.ProcessID); pathErr == nil && strings.EqualFold(path, exe) {
			count++
		}
	}
	return count, nil
}

func processPath(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process) //nolint:errcheck // nothing to do about it.
	size := uint32(windows.MAX_LONG_PATH)
	buffer := make([]uint16, size)
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}
