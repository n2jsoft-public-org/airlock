package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPathsInRefusesSocketPathsTooLong(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	if _, err := PathsIn(long); err == nil || !strings.Contains(err.Error(), "daemon.sock") {
		t.Fatalf("err = %v, want the socket path named", err)
	}
	paths, err := PathsIn(shortDir(t))
	if err != nil || filepath.Base(paths.Socket) != "daemon.sock" || filepath.Base(paths.Lock) != "daemon.lock" {
		t.Fatalf("paths = %+v, %v", paths, err)
	}
}
