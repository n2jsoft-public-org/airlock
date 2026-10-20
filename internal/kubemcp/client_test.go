package kubemcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// caOf is the base64 PEM certificate of server, as describe-cluster prints a cluster CA.
func caOf(server *httptest.Server) string {
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
}

// foreignCA is a base64 PEM certificate authority nothing in the test is signed by.
func foreignCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "foreign"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// testClient serves handler over TLS and returns a client trusting it; httptest certificates are
// valid for example.com, which stands for the real EKS endpoint host.
func testClient(t *testing.T, handler http.Handler, fetch tokenFunc) *apiClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := newAPIClient(server.URL, "example.com", caOf(server), newTokenSource(fetch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.close)
	return client
}

func staticTokens() tokenFunc {
	fetch, _ := fakeTokens(time.Now().Add(time.Hour), "tok")
	return fetch
}

func TestAPIClientSendsBearerTokenAndVerifiesTheCA(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("limit") != "5" {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}
		//nolint:errcheck // test intentionally ignores errors
		_, _ = w.Write([]byte(`{"kind":"NamespaceList","items":[]}`))
	}), staticTokens())

	var list struct {
		Kind string `json:"kind"`
	}
	if err := client.getJSON(context.Background(), "/api/v1/namespaces", map[string][]string{"limit": {"5"}}, &list); err != nil || list.Kind != "NamespaceList" {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
}

func TestAPIClientRejectsAnUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	client, err := newAPIClient(server.URL, "example.com", foreignCA(t), newTokenSource(staticTokens()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	if _, err := client.get(context.Background(), "/api", nil); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want a certificate error", err)
	}
}

func TestAPIClientRetriesOnceWithFreshTokenAfter401(t *testing.T) {
	fetch, calls := fakeTokens(time.Now().Add(time.Hour), "expired", "fresh")
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			//nolint:errcheck // test intentionally ignores errors
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		//nolint:errcheck // test intentionally ignores errors
		_, _ = w.Write([]byte(`{"ok":true}`))
	}), fetch)

	if body, err := client.get(context.Background(), "/api", nil); err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("body = %s, err = %v", body, err)
	}
	if *calls != 2 {
		t.Fatalf("token fetched %d times, want 2", *calls)
	}
}

func TestAPIClientMapsStatuses(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
			//nolint:errcheck // test intentionally ignores errors
			_, _ = w.Write([]byte(`{"kind":"Status","message":"secrets is forbidden: User \"dev\" cannot list resource \"secrets\""}`))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			//nolint:errcheck // test intentionally ignores errors
			_, _ = w.Write([]byte(`{"kind":"Status","message":"pods \"api-9\" not found"}`))
		case "/unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			//nolint:errcheck // test intentionally ignores errors
			_, _ = w.Write([]byte("etcd timeout"))
		}
	}), staticTokens())
	ctx := context.Background()

	if _, err := client.get(ctx, "/forbidden", nil); err == nil || err.Error() != `forbidden by the cluster RBAC: secrets is forbidden: User "dev" cannot list resource "secrets"` {
		t.Fatalf("403: %v", err)
	}
	if _, err := client.get(ctx, "/missing", nil); !errors.Is(err, errNotFound) || !strings.Contains(err.Error(), `pods "api-9" not found`) {
		t.Fatalf("404: %v", err)
	}
	if _, err := client.get(ctx, "/unauthorized", nil); err == nil || !strings.Contains(err.Error(), "EKS access entry") {
		t.Fatalf("401: %v", err)
	}
	if _, err := client.get(ctx, "/boom", nil); err == nil || err.Error() != "the Kubernetes API answered 500: etcd timeout" {
		t.Fatalf("500: %v", err)
	}
}

func TestNewAPIClientRejectsInvalidCA(t *testing.T) {
	if _, err := newAPIClient("https://localhost:1", "example.com", "not base64!", newTokenSource(staticTokens())); err == nil {
		t.Fatal("expected an error for an invalid CA")
	}
	if _, err := newAPIClient("https://localhost:1", "example.com", base64.StdEncoding.EncodeToString([]byte("no pem")), newTokenSource(staticTokens())); err == nil {
		t.Fatal("expected an error for a CA without PEM certificate")
	}
}
