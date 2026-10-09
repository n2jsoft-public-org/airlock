// Package selfupdatetest fakes the GitHub releases API and builds release archives for the tests
// of airlock update.
package selfupdatetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// AssetName is the archive name .goreleaser.yaml gives the build of tag for goos/goarch.
func AssetName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "airlock_" + tag + "_" + goos + "_" + goarch + ext
}

// Archive returns an archive like the ones .goreleaser.yaml builds for goos: a tar.gz (a zip on
// Windows) holding README.md and the airlock binary, whose content is binary.
func Archive(t *testing.T, goos string, binary []byte) []byte {
	t.Helper()
	name := "airlock"
	if goos == "windows" {
		name = "airlock.exe"
	}
	files := []struct {
		name string
		data []byte
	}{{"README.md", []byte("# airlock\n")}, {name, binary}}

	var buf bytes.Buffer
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		for _, f := range files {
			w, err := zw.Create(f.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(f.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Release is the latest release a fake GitHub serves.
type Release struct {
	// Assets maps each asset name to its content.
	Assets map[string][]byte
	Tag    string
	ID     int64
	// RefuseReactions answers 403 to reactions, as GitHub does for a read-only token.
	RefuseReactions bool
}

// NewRelease returns the release tag with the archive of binary for goos/goarch and its
// checksums.txt.
func NewRelease(t *testing.T, tag, goos, goarch string, binary []byte) Release {
	t.Helper()
	archive := Archive(t, goos, binary)
	name := AssetName(tag, goos, goarch)
	sum := sha256.Sum256(archive)
	return Release{ID: 42, Tag: tag, Assets: map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"),
	}}
}

// Repository and Token are the repository the fake server serves and the token it accepts.
const (
	Repository = "acme/airlock"
	Token      = "test-token"
)

// Server is a fake GitHub API serving Repository. A nil Release means the repository has no
// release yet.
type Server struct {
	*httptest.Server
	Release   *Release
	requests  []string
	reactions []string
	mu        sync.Mutex
}

// NewServer starts a fake GitHub API serving release; it stops with the test.
func NewServer(t *testing.T, release *Release) *Server {
	t.Helper()
	s := &Server{Release: release}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Reactions returns the reactions added to the release so far, in order, as "<release id>:<content>".
func (s *Server) Reactions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reactions...)
}

// Paths returns the paths requested so far, in order.
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	s.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+Token {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	repo := "/repos/" + Repository
	switch {
	case r.URL.Path == repo:
		writeJSON(w, map[string]string{"full_name": Repository})
	case r.URL.Path == repo+"/releases/latest" && s.Release != nil:
		assets := make([]map[string]string, 0, len(s.Release.Assets))
		for name := range s.Release.Assets {
			assets = append(assets, map[string]string{"name": name, "url": s.URL + "/assets/" + name})
		}
		writeJSON(w, map[string]any{"id": s.Release.ID, "tag_name": s.Release.Tag, "assets": assets})
	case r.Method == http.MethodPost && s.Release != nil && r.URL.Path == fmt.Sprintf("%s/releases/%d/reactions", repo, s.Release.ID):
		s.react(w, r)
	case strings.HasPrefix(r.URL.Path, "/assets/") && s.Release != nil && r.Header.Get("Accept") == "application/octet-stream":
		data, ok := s.Release.Assets[strings.TrimPrefix(r.URL.Path, "/assets/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write(data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) react(w http.ResponseWriter, r *http.Request) {
	if s.Release.RefuseReactions {
		http.Error(w, `{"message":"Resource not accessible by personal access token"}`, http.StatusForbidden)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, `{"message":"Invalid request"}`, http.StatusUnprocessableEntity)
		return
	}
	s.mu.Lock()
	s.reactions = append(s.reactions, fmt.Sprintf("%d:%s", s.Release.ID, body.Content))
	s.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
