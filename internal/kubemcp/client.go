package kubemcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// requestTimeout bounds every request to the cluster, so a tool call never hangs.
	requestTimeout = 30 * time.Second
	// maxAPIResponseBytes refuses API answers too large to handle in memory.
	maxAPIResponseBytes = 32 << 20
)

// errNotFound reports an object or path the cluster does not have.
var errNotFound = errors.New("not found")

// apiClient reads the Kubernetes API through the tunnel, trusting only the cluster CA for the real
// endpoint host.
type apiClient struct {
	http    *http.Client
	tokens  *tokenSource
	baseURL string
}

func newAPIClient(baseURL, serverName, caData string, tokens *tokenSource) (*apiClient, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(caData))
	if err != nil {
		return nil, fmt.Errorf("invalid cluster certificate authority: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("invalid cluster certificate authority: no PEM certificate")
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12},
		// The tunnel is on localhost: never route it through a proxy from the environment.
		Proxy: nil,
	}
	return &apiClient{
		http:    &http.Client{Transport: transport, Timeout: requestTimeout},
		tokens:  tokens,
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}, nil
}

// get reads path with query. A refused token is renewed and the request retried once.
func (c *apiClient) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	body, status, err := c.do(ctx, path, query)
	if err == nil && status == http.StatusUnauthorized {
		c.tokens.reset()
		body, status, err = c.do(ctx, path, query)
	}
	if err != nil {
		return nil, err
	}
	return body, statusError(status, body)
}

// getJSON reads path with query into v.
func (c *apiClient) getJSON(ctx context.Context, path string, query url.Values, v any) error {
	body, err := c.get(ctx, path, query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("unexpected answer from the Kubernetes API for %s: %w", path, err)
	}
	return nil
}

func (c *apiClient) do(ctx context.Context, path string, query url.Values) (body []byte, status int, err error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, 0, err
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("the Kubernetes API is not reachable through the tunnel: %w", err)
	}
	//nolint:errcheck // close error is not actionable
	defer func() { _ = response.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read the Kubernetes API answer: %w", err)
	}
	if len(body) > maxAPIResponseBytes {
		return nil, 0, errors.New("the Kubernetes API answer is over 32 MiB: narrow it with a namespace, selectors or a limit")
	}
	return body, response.StatusCode, nil
}

func statusError(status int, body []byte) error {
	if status < http.StatusMultipleChoices {
		return nil
	}
	message := apiMessage(body)
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("the cluster refused the AWS IAM token: is your IAM identity mapped to the cluster (EKS access entry or aws-auth)? %s", message)
	case http.StatusForbidden:
		return fmt.Errorf("forbidden by the cluster RBAC: %s", message)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", errNotFound, message)
	default:
		return fmt.Errorf("the Kubernetes API answered %d: %s", status, message)
	}
}

// apiMessage is the message of a Kubernetes Status answer, else the start of the raw answer.
func apiMessage(body []byte) string {
	var status struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &status) == nil && status.Message != "" {
		return status.Message
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// close drops the idle connections of the client.
func (c *apiClient) close() {
	c.http.CloseIdleConnections()
}
