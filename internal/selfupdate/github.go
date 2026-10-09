package selfupdate

import (
	"bytes"
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

// InstallReaction is the reaction airlock adds to the release it installs, so the releases tell
// who installed them.
const InstallReaction = "rocket"

// Release is a GitHub release.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
	ID     int64   `json:"id"`
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

// sameHostAsAPI tells whether target is served by the GitHub API host: any other host gets no token.
func (c *Client) sameHostAsAPI(target *url.URL) bool {
	base, err := url.Parse(c.BaseURL)
	return err == nil && target.Host == base.Host
}

func (c *Client) get(ctx context.Context, target, accept string) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if c.sameHostAsAPI(req.URL) {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
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

// React adds the reaction content (such as InstallReaction) to release, as the token's account.
// GitHub keeps one reaction of each kind per account: adding it again changes nothing.
func (c *Client) React(ctx context.Context, release Release, content string) (err error) {
	target := c.repoURL(fmt.Sprintf("/releases/%d/reactions", release.ID))
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("GitHub answered %s for %s", resp.Status, req.URL.Path)
	}
	return nil
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

// Download returns the content of a release asset.
func (c *Client) Download(ctx context.Context, asset Asset) ([]byte, error) {
	body, err := c.get(ctx, asset.URL, "application/octet-stream")
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("cannot download %s: not found", asset.Name)
	}
	return body, err
}
