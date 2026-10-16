package orgconfig

import (
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		raw  string
		want Source
	}{
		{"/etc/acme/airlock.json", Source{Kind: KindFile, Path: "/etc/acme/airlock.json"}},
		{"https://config.acme.test/a.json", Source{Kind: KindHTTPS, URL: "https://config.acme.test/a.json"}},
		{"https://github.com/acme/infra/blob/main/airlock/airlock.json", Source{Kind: KindGitHub, Owner: "acme", Repo: "infra", Ref: "main", File: "airlock/airlock.json"}},
		{"https://GitHub.com/acme/infra/blob/main/a.json", Source{Kind: KindGitHub, Owner: "acme", Repo: "infra", Ref: "main", File: "a.json"}},
		{"https://github.com/acme/infra/blob/feature/x/a.json", Source{Kind: KindGitHub, Owner: "acme", Repo: "infra", Ref: "feature", File: "x/a.json"}},
		{"https://acme.ghe.com/acme/infra/blob/main/a.json", Source{Kind: KindHTTPS, URL: "https://acme.ghe.com/acme/infra/blob/main/a.json"}},
		{"https://www.github.com/acme/infra/blob/main/a.json", Source{Kind: KindHTTPS, URL: "https://www.github.com/acme/infra/blob/main/a.json"}},
		{"https://github.com:443/acme/infra/blob/main/a.json", Source{Kind: KindGitHub, Owner: "acme", Repo: "infra", Ref: "main", File: "a.json"}},
	}
	for _, tt := range tests {
		got, err := ParseSource(tt.raw)
		if err != nil {
			t.Errorf("ParseSource(%q): %v", tt.raw, err)
			continue
		}
		tt.want.Raw = tt.raw
		if tt.want.Kind == KindGitHub {
			tt.want.URL = tt.raw
		}
		if got != tt.want {
			t.Errorf("ParseSource(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}
}

func TestParseSourceErrors(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/acme/infra",
		"https://github.com/acme/infra/tree/main/a.json",
		"https://github.com/acme/infra/blob/main",
		"http://x.test/a.json",
		"a.json",
		"",
		"https://user:s3cret@config.acme.test/a.json",
		"https://user:s3cret@github.com/acme/infra/blob/main/a.json",
		"http://user:s3cret@x.test/a.json",
		"https://ghp_s3cret@config.acme.test/a.json",
		"https://ghp_s3cret@github.com/acme/infra/blob/main/a.json",
		"http://ghp_s3cret@x.test/a.json",
		"https://github.com/acme/infra?token=s3cret#s3cret",
	} {
		_, err := ParseSource(raw)
		if err == nil {
			t.Errorf("ParseSource(%q): want an error", raw)
		} else if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("ParseSource(%q) error leaks the password: %v", raw, err)
		}
	}
}

func TestSourceDisplayHidesQueryAndFragment(t *testing.T) {
	tests := map[string]string{
		"/etc/acme/airlock.json":                                      "/etc/acme/airlock.json",
		"https://config.acme.test/a.json?sig=secret#frag":             "https://config.acme.test/a.json",
		"https://github.com/acme/infra/blob/main/a.json?token=secret": "https://github.com/acme/infra/blob/main/a.json",
	}
	for raw, want := range tests {
		src, err := ParseSource(raw)
		if err != nil {
			t.Fatalf("ParseSource(%q): %v", raw, err)
		}
		if got := src.Display(); got != want {
			t.Errorf("Display(%q) = %q, want %q", raw, got, want)
		}
	}
}
