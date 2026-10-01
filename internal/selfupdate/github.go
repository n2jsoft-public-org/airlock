package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the GitHub REST API.
const DefaultBaseURL = "https://api.github.com"

// maxResponse caps a GitHub response or download; an airlock archive is about 10 MB.
const maxResponse = 200 << 20

// Release is a GitHub release.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Asset is a file attached to a release; URL is its API address.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Client reads the releases of one GitHub repository (owner/repo) with a token.
type Client struct {
	HTTP       *http.Client
	BaseURL    string
	Repository string
	Token      string
}

// NewClient returns a client of the GitHub API at baseURL (DefaultBaseURL outside tests).
func NewClient(baseURL, repository, token string) *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: 2 * time.Minute, CheckRedirect: dropTokenOnOtherHost},
		BaseURL:    strings.TrimSuffix(baseURL, "/"),
		Repository: repository,
		Token:      token,
	}
}

// dropTokenOnOtherHost keeps the token from the storage host GitHub redirects downloads to.
func dropTokenOnOtherHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

var errNotFound = errors.New("not found")

func (c *Client) accessError() error {
	return fmt.Errorf("cannot read releases of %s: check the token can access it", c.Repository)
}

func (c *Client) repoURL(path string) string {
	return c.BaseURL + "/repos/" + c.Repository + path
}

func (c *Client) get(ctx context.Context, target, accept string) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, c.accessError()
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s for %s", resp.Status, req.URL.Path)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the GitHub response: %w", err)
	}
	if len(body) > maxResponse {
		return nil, fmt.Errorf("the GitHub response for %s is too large", req.URL.Path)
	}
	return body, nil
}

// LatestRelease returns the latest release of the repository.
func (c *Client) LatestRelease(ctx context.Context) (Release, error) {
	body, err := c.get(ctx, c.repoURL("/releases/latest"), "application/vnd.github+json")
	if errors.Is(err, errNotFound) {
		// GitHub answers 404 both without access and without a release: the repository tells which.
		if _, repoErr := c.get(ctx, c.repoURL(""), "application/vnd.github+json"); repoErr != nil {
			if errors.Is(repoErr, errNotFound) {
				return Release{}, c.accessError()
			}
			return Release{}, repoErr
		}
		return Release{}, fmt.Errorf("%s has no release yet", c.Repository)
	}
	if err != nil {
		return Release{}, err
	}
	var release Release
	if err = json.Unmarshal(body, &release); err != nil {
		return Release{}, fmt.Errorf("cannot read the latest release: %w", err)
	}
	return release, nil
}

// Compare returns GitHub's status of base...head (CompareIdentical, CompareAhead, CompareBehind or
// CompareDiverged), or CompareUnknown when GitHub does not know one of the commits.
func (c *Client) Compare(ctx context.Context, base, head string) (string, error) {
	target := c.repoURL("/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?per_page=1")
	body, err := c.get(ctx, target, "application/vnd.github+json")
	if errors.Is(err, errNotFound) {
		return CompareUnknown, nil
	}
	if err != nil {
		return "", err
	}
	var comparison struct {
		Status string `json:"status"`
	}
	if err = json.Unmarshal(body, &comparison); err != nil {
		return "", fmt.Errorf("cannot read the commit comparison: %w", err)
	}
	return comparison.Status, nil
}

// Download returns the content of a release asset.
func (c *Client) Download(ctx context.Context, asset Asset) ([]byte, error) {
	body, err := c.get(ctx, asset.URL, "application/octet-stream")
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("cannot download %s: not found", asset.Name)
	}
	return body, err
}
