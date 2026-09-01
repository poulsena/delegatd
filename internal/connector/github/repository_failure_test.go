package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositorySnapshotMapsInstallationAndAPIFailures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		headers    map[string]string
		wantReason string
	}{
		{name: "missing installation", status: http.StatusNotFound, wantReason: "repository is not installed"},
		{name: "rate limited", status: http.StatusTooManyRequests, wantReason: "repository is unavailable"},
		{name: "redirect", status: http.StatusFound, wantReason: "repository is unavailable"},
		{name: "server failure", status: http.StatusBadGateway, wantReason: "repository is unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			keyPath := writePrivateKey(t, dir, key, "pkcs1")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/api/installation" {
					t.Errorf("unexpected request after failed installation: %s", r.URL.Path)
				}
				for name, value := range tc.headers {
					w.Header().Set(name, value)
				}
				if tc.status == http.StatusFound {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("api-body-secret-sentinel"))
			}))
			defer server.Close()
			source, err := newRepositorySource(Config{AppID: 1, PrivateKeyFile: filepath.Base(keyPath)}, RepositoryConfig{ExternalRef: "acme/api"}, dir, repositoryOptions{
				now:          func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
				client:       server.Client(),
				baseURL:      server.URL,
				totalTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Snapshot(context.Background()); err == nil || err.Error() != tc.wantReason || strings.Contains(err.Error(), "api-body-secret-sentinel") {
				t.Fatalf("Snapshot() error = %v, want %q without body sentinel", err, tc.wantReason)
			}
		})
	}
}

func TestRepositorySnapshotRejectsOverScopedAndExpiredTokens(t *testing.T) {
	tests := []struct {
		name        string
		expiresAt   string
		permissions string
	}{
		{name: "over scoped", expiresAt: "2026-09-01T13:00:00Z", permissions: `{"contents":"read","metadata":"read","issues":"read"}`},
		{name: "expired", expiresAt: "2026-09-01T11:59:59Z", permissions: `{"contents":"read","metadata":"read"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, server := newTokenFailureSource(t, tc.expiresAt, tc.permissions)
			defer server.Close()
			if _, err := source.Snapshot(context.Background()); err == nil || err.Error() != "repository is unavailable" {
				t.Fatalf("Snapshot() error = %v", err)
			}
		})
	}
}

func TestRepositorySnapshotMapsInvalidAndOversizedConfiguration(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid", body: []byte("version: 1\nunknown: body-secret-sentinel\n")},
		{name: "oversized", body: []byte(strings.Repeat("x", maxAPIResponseSize+1))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, server := newCompleteRepositorySource(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/acme/api/contents/.delegatd.yaml" {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(tc.body)
					return
				}
				writeCompleteRepositoryResponse(w, r)
			})
			defer server.Close()
			if _, err := source.Snapshot(context.Background()); err == nil || err.Error() != "repository configuration is invalid" || strings.Contains(err.Error(), "body-secret-sentinel") {
				t.Fatalf("Snapshot() error = %v", err)
			}
		})
	}
}

func TestRepositorySnapshotMapsResponseBodyIOFailure(t *testing.T) {
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := writePrivateKey(t, dir, key, "pkcs1")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       errorBody{err: errors.New("response-body-secret-sentinel")},
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	source, err := newRepositorySource(Config{AppID: 1, PrivateKeyFile: filepath.Base(keyPath)}, RepositoryConfig{ExternalRef: "acme/api"}, dir, repositoryOptions{
		now:            time.Now,
		client:         client,
		baseURL:        "https://github.invalid",
		totalTimeout:   time.Second,
		requestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Snapshot(context.Background()); err == nil || err.Error() != "repository is unavailable" || strings.Contains(err.Error(), "response-body-secret-sentinel") {
		t.Fatalf("Snapshot() error = %v", err)
	}
}

func TestRepositorySnapshotMapsHeaderAndBodyTimeouts(t *testing.T) {
	for _, stalledBody := range []bool{false, true} {
		t.Run(map[bool]string{false: "header", true: "body"}[stalledBody], func(t *testing.T) {
			source, server := newTimeoutRepositorySource(t, stalledBody)
			defer server.Close()
			if _, err := source.Snapshot(context.Background()); err == nil || err.Error() != "repository is unavailable" {
				t.Fatalf("Snapshot() error = %v", err)
			}
		})
	}
}

func newTokenFailureSource(t *testing.T, expiresAt, permissions string) (*RepositorySource, *httptest.Server) {
	t.Helper()
	return newCompleteRepositorySource(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/api/installation":
			_, _ = w.Write([]byte(`{"id":7}`))
		case "/app/installations/7/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"token","expires_at":"` + expiresAt + `","permissions":` + permissions + `}`))
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
}

func newCompleteRepositorySource(t *testing.T, handler http.HandlerFunc) (*RepositorySource, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := writePrivateKey(t, dir, key, "pkcs1")
	server := httptest.NewServer(handler)
	source, err := newRepositorySource(Config{AppID: 1, PrivateKeyFile: filepath.Base(keyPath)}, RepositoryConfig{ExternalRef: "acme/api"}, dir, repositoryOptions{
		now:            func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
		client:         server.Client(),
		baseURL:        server.URL,
		totalTimeout:   time.Second,
		requestTimeout: time.Second,
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return source, server
}

func writeCompleteRepositoryResponse(w http.ResponseWriter, r *http.Request) {
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
		_, _ = w.Write([]byte("version: 1\n"))
	default:
		http.NotFound(w, r)
	}
}

func newTimeoutRepositorySource(t *testing.T, stalledBody bool) (*RepositorySource, *httptest.Server) {
	t.Helper()
	if !stalledBody {
		return newCompleteRepositorySource(t, func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
	}
	return newCompleteRepositorySource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errorBody struct {
	err error
}

func (b errorBody) Read([]byte) (int, error) {
	return 0, b.err
}

func (b errorBody) Close() error {
	return nil
}

var _ io.ReadCloser = errorBody{}
