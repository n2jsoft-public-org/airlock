package orgconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// MaxSize is the largest organization config accepted, from any source.
const MaxSize = 1 << 20

// defaultGitHubAPI is the GitHub REST API.
const defaultGitHubAPI = "https://api.github.com"

// Sentinels the caller turns into warnings.
var (
	ErrAccess      = i18n.NewError("orgconfig.fetch.access")
	ErrTooLarge    = i18n.NewError("orgconfig.fetch.too_large")
	ErrUnreachable = i18n.NewError("orgconfig.fetch.unreachable")
)

// Fetcher reads a Source. A nil HTTP is a default client: timeouts come from the context.
type Fetcher struct {
	HTTP *http.Client
	// Token returns the GitHub token; it is sent to GitHub sources only.
	Token     func(context.Context) (string, error)
	GitHubAPI string // defaults to https://api.github.com
}

// Fetched is the body of a source and the ETag to send next time.
type Fetched struct {
	ETag        string
	Body        []byte
	NotModified bool
}

// Fetch reads src. A non-empty etag asks the server to answer NotModified when nothing changed.
func (f *Fetcher) Fetch(ctx context.Context, src Source, etag string) (_ Fetched, err error) { //nolint:gocritic // Source by value is the public contract.
	if src.Kind == KindFile {
		return fetchFile(src.Path)
	}
	req, err := f.request(ctx, &src, etag)
	if err != nil {
		return Fetched{}, err
	}
	resp, err := f.client().Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err // url.Error would print the request URL, query included
		}
		return Fetched{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return Fetched{NotModified: true, ETag: etag}, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden,
		resp.StatusCode == http.StatusNotFound && src.Kind == KindGitHub:
		return Fetched{}, ErrAccess
	case resp.StatusCode != http.StatusOK:
		return Fetched{}, fmt.Errorf("%w: HTTP %d", ErrUnreachable, resp.StatusCode)
	}
	body, err := readLimited(resp.Body)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Body: body, ETag: resp.Header.Get("ETag")}, nil
}

func (f *Fetcher) request(ctx context.Context, src *Source, etag string) (*http.Request, error) {
	target := src.URL
	if src.Kind == KindGitHub {
		api := strings.TrimSuffix(f.GitHubAPI, "/")
		if api == "" {
			api = defaultGitHubAPI
		}
		segments := strings.Split(src.File, "/")
		for i, s := range segments {
			segments[i] = url.PathEscape(s)
		}
		target = fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", api,
			url.PathEscape(src.Owner), url.PathEscape(src.Repo), strings.Join(segments, "/"), url.QueryEscape(src.Ref))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		u, _ := url.Parse(target) //nolint:errcheck // a nil URL redacts to "".
		return nil, fmt.Errorf("%w: %s", ErrUnreachable, redact(u))
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if src.Kind == KindGitHub {
		if f.Token == nil {
			return nil, ErrAccess
		}
		token, err := f.Token(ctx)
		if err != nil || token == "" {
			return nil, ErrAccess
		}
		req.Header.Set("Accept", "application/vnd.github.raw")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	return req, nil
}

// client is f.HTTP (or a default one) with the redirect policy applied.
func (f *Fetcher) client() *http.Client {
	c := &http.Client{}
	if f.HTTP != nil {
		c = new(*f.HTTP)
	}
	c.CheckRedirect = dropTokenOnOtherHost
	return c
}

// dropTokenOnOtherHost keeps the token from any host the request is redirected to.
func dropTokenOnOtherHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return errors.New("redirect to a non-https URL refused")
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

func fetchFile(path string) (_ Fetched, err error) {
	file, err := os.Open(path) //nolint:gosec // the path is the organizationConfig the developer chose.
	if err != nil {
		return Fetched{}, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	body, err := readLimited(file)
	return Fetched{Body: body}, err
}

func readLimited(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if len(body) > MaxSize {
		return nil, ErrTooLarge
	}
	return body, nil
}
