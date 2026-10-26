package orgconfig

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// Kind tells where a Source is read from.
type Kind int

const (
	KindFile   Kind = iota // absolute path
	KindHTTPS              // any https:// URL
	KindGitHub             // https://github.com/<owner>/<repo>/blob/<ref>/<file>
)

// Source is a parsed organizationConfig value.
type Source struct {
	Raw  string
	Path string // KindFile
	URL  string // KindHTTPS and KindGitHub: the raw link
	// Owner, Repo, Ref and File locate a KindGitHub file. Ref is the first segment after blob, so
	// a branch name containing a slash is not supported.
	Owner, Repo, Ref, File string
	Kind                   Kind
}

// ParseSource classifies raw: an absolute path, an https:// URL, or a github.com file link.
// Errors never echo URL credentials.
func ParseSource(raw string) (Source, error) {
	raw = strings.TrimSpace(raw)
	if filepath.IsAbs(raw) {
		return Source{Raw: raw, Kind: KindFile, Path: raw}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Source{}, i18n.NewError("orgconfig.source.invalid", redact(u))
	}
	if u.User != nil {
		return Source{}, i18n.NewError("orgconfig.source.credentials", u.Host)
	}
	s := Source{Raw: raw, Kind: KindHTTPS, URL: raw}
	if strings.EqualFold(u.Hostname(), "github.com") {
		// ["", owner, repo, "blob", ref, file...] once split on "/"
		parts := strings.SplitN(u.Path, "/", 6)
		if len(parts) < 6 || parts[3] != "blob" || parts[1] == "" || parts[2] == "" || parts[4] == "" || parts[5] == "" {
			return Source{}, i18n.NewError("orgconfig.source.not_file_url", redact(u))
		}
		s.Kind, s.Owner, s.Repo, s.Ref, s.File = KindGitHub, parts[1], parts[2], parts[4], parts[5]
	}
	return s, nil
}

// Display is s safe to print: the path of a file, a URL without userinfo, query or fragment.
func (s Source) Display() string { //nolint:gocritic // Source by value is the public contract.
	if s.Kind == KindFile {
		return s.Path
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return ""
	}
	return redact(u)
}

// redact is the URL without userinfo (a token may be the username), query or fragment, or ""
// when it did not parse: safe to print.
func redact(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User, c.RawQuery, c.Fragment = nil, "", ""
	return c.String()
}
