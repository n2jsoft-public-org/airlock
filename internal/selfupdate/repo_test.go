package selfupdate

import "testing"

func TestRepositoryFromModule(t *testing.T) {
	for path, want := range map[string]string{
		"github.com/acme/airlock":        "acme/airlock",
		"github.com/acme/airlock/v2":     "acme/airlock",
		"github.com/acme/airlock/cmd/ai": "acme/airlock",
	} {
		got, err := RepositoryFromModule(path)
		if err != nil || got != want {
			t.Errorf("RepositoryFromModule(%q) = %q, %v, want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"", "gitlab.com/acme/airlock", "github.com/acme", "command-line-arguments"} {
		_, err := RepositoryFromModule(path)
		if err == nil || err.Error() != "cannot tell where airlock is released from (module "+path+")" {
			t.Errorf("RepositoryFromModule(%q) error = %v", path, err)
		}
	}
}
