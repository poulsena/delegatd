package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositorySnapshotReadsReadOnlySourceAndPinsRevision(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := writePrivateKey(t, dir, key, "pkcs1")
	fixedNow := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	const commitSHA = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2026-03-10" {
			t.Errorf("API version = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "delegatd/task" {
			t.Errorf("User-Agent = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/api_service/installation":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("installation request has no bearer token")
			}
			_, _ = w.Write([]byte(`{"id":99}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/99/access_tokens":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("token request body: %v", err)
			}
			if got := body["repositories"].([]any)[0]; got != "api_service" {
				t.Errorf("repositories = %#v", body["repositories"])
			}
			permissions := body["permissions"].(map[string]any)
			if len(permissions) != 2 || permissions["contents"] != "read" || permissions["metadata"] != "read" {
				t.Errorf("permissions = %#v", permissions)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"installation-secret","expires_at":"2026-09-01T13:00:00Z","permissions":{"contents":"read","metadata":"read"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/api_service":
			_, _ = w.Write([]byte(`{"id":123,"full_name":"Acme/Api_Service","default_branch":"feature/name"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/api_service/commits":
			if got := r.URL.Query().Get("sha"); got != "feature/name" {
				t.Errorf("sha query = %q", got)
			}
			if got := r.URL.Query().Get("per_page"); got != "1" {
				t.Errorf("per_page query = %q", got)
			}
			_, _ = w.Write([]byte(`[{"sha":"` + commitSHA + `"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/api_service/contents/.delegatd.yaml":
			if got := r.URL.Query().Get("ref"); got != strings.ToLower(commitSHA) {
				t.Errorf("ref query = %q", got)
			}
			if got := r.Header.Get("Accept"); got != "application/vnd.github.raw+json" {
				t.Errorf("Accept = %q", got)
			}
			_, _ = w.Write([]byte("version: 1\nagent:\n  instructions: [AGENTS.md]\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source, err := newRepositorySource(Config{AppID: 123, PrivateKeyFile: filepath.Base(keyPath)}, RepositoryConfig{ExternalRef: "acme/api_service"}, dir, repositoryOptions{
		now:            func() time.Time { return fixedNow },
		client:         server.Client(),
		baseURL:        server.URL,
		totalTimeout:   time.Second,
		requestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newRepositorySource() error = %v", err)
	}
	material, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if material.ExternalIdentity != "123" || material.ExternalRef != "Acme/Api_Service" || material.Revision != strings.ToLower(commitSHA) {
		t.Fatalf("material = %#v", material)
	}
	if got := material.Configuration.Agent.Instructions; len(got) != 1 || got[0] != "AGENTS.md" {
		t.Fatalf("configuration = %#v", material.Configuration)
	}
	serialized, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "installation-secret") || strings.Contains(string(serialized), "private") {
		t.Fatalf("material leaked credential data: %s", serialized)
	}
	wantRequests := []string{
		"GET /repos/acme/api_service/installation",
		"POST /app/installations/99/access_tokens",
		"GET /repos/acme/api_service",
		"GET /repos/acme/api_service/commits?per_page=1&sha=feature%2Fname",
		"GET /repos/acme/api_service/contents/.delegatd.yaml?ref=" + strings.ToLower(commitSHA),
	}
	if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
		t.Fatalf("requests = %v, want %v", requests, wantRequests)
	}
}

func TestRepositorySnapshotUsesEmptyDefaultWhenConfigurationIsMissing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := writePrivateKey(t, dir, key, "pkcs8")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/api/installation":
			_, _ = w.Write([]byte(`{"id":7}`))
		case "/app/installations/7/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"token","expires_at":"2026-09-01T13:00:00Z","permissions":{"contents":"read","metadata":"read"}}`))
		case "/repos/acme/api":
			_, _ = w.Write([]byte(`{"id":8,"full_name":"acme/api","default_branch":"main"}`))
		case "/repos/acme/api/commits":
			_, _ = w.Write([]byte(`[{"sha":"0123456789abcdef0123456789abcdef01234567"}]`))
		case "/repos/acme/api/contents/.delegatd.yaml":
			w.WriteHeader(http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	source, err := newRepositorySource(Config{AppID: 1, PrivateKeyFile: filepath.Base(keyPath)}, RepositoryConfig{ExternalRef: "acme/api"}, dir, repositoryOptions{
		now:          func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
		client:       server.Client(),
		baseURL:      server.URL,
		totalTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newRepositorySource() error = %v", err)
	}
	material, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if material.Configuration.Version != 1 || material.Configuration.Agent.Instructions == nil || material.Configuration.Policy.Actions == nil || material.Configuration.Validation.Required == nil {
		t.Fatalf("default configuration = %#v", material.Configuration)
	}
}

func TestRepositorySnapshotRejectsInvalidResourceAndTimeout(t *testing.T) {
	dir := t.TempDir()
	if _, err := newRepositorySource(Config{AppID: 1, PrivateKeyFile: "key.pem"}, RepositoryConfig{ExternalRef: "https://github.com/acme/api"}, dir, repositoryOptions{}); err == nil {
		t.Fatal("invalid external_ref accepted")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := writePrivateKey(t, dir, key, "pkcs1")
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	source, err := newRepositorySource(Config{AppID: 1, PrivateKeyFile: filepath.Base(keyPath)}, RepositoryConfig{ExternalRef: "acme/api"}, dir, repositoryOptions{
		now:            time.Now,
		client:         server.Client(),
		baseURL:        server.URL,
		totalTimeout:   20 * time.Millisecond,
		requestTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newRepositorySource() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := source.Snapshot(ctx); err == nil || !strings.Contains(err.Error(), "repository is unavailable") {
		t.Fatalf("timeout error = %v", err)
	}
	_ = os.Remove(keyPath)
}
