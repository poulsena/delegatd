package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDoctorCheckAuthenticatesWithVerifiedJWTAndGETOnly(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := writePrivateKey(t, t.TempDir(), key, "pkcs1")
	const appID int64 = 123456
	fixedNow := time.Unix(1_700_000_000, 0)
	var method, requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, requestPath = r.Method, r.URL.Path
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2026-03-10" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "delegatd/doctor" {
			t.Errorf("User-Agent = %q", got)
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		verifyJWT(t, jwt, &key.PublicKey, fixedNow, appID)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%d}`, appID)
	}))
	defer server.Close()

	check := newDoctorCheck("github-main", Config{AppID: appID, PrivateKeyFile: filepath.Base(keyPath)}, filepath.Dir(keyPath), doctorOptions{
		now:     func() time.Time { return fixedNow },
		client:  server.Client(),
		baseURL: server.URL,
	})
	detail, failure := check.Probe(context.Background())
	if failure != nil {
		t.Fatalf("failure = %v", failure)
	}
	if detail != "GitHub App authenticated" {
		t.Fatalf("detail = %q", detail)
	}
	if method != http.MethodGet || requestPath != "/app" {
		t.Fatalf("request = %s %s, want GET /app", method, requestPath)
	}
}

func TestDoctorCheckSupportsPKCS1AndPKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"pkcs1", "pkcs8"} {
		t.Run(format, func(t *testing.T) {
			path := writePrivateKey(t, t.TempDir(), key, format)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"id":7}`)
			}))
			defer server.Close()
			check := newDoctorCheck("main", Config{AppID: 7, PrivateKeyFile: filepath.Base(path)}, filepath.Dir(path), doctorOptions{
				now:     time.Now,
				client:  server.Client(),
				baseURL: server.URL,
			})
			if detail, failure := check.Probe(context.Background()); failure != nil || detail != "GitHub App authenticated" {
				t.Fatalf("detail=%q failure=%v", detail, failure)
			}
		})
	}
}

func TestDoctorCheckMapsAuthenticationAndResponseFailures(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := writePrivateKey(t, t.TempDir(), key, "pkcs1")
	for _, tc := range []struct {
		name       string
		status     int
		headers    map[string]string
		body       string
		wantReason string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"id":1}`, wantReason: "GitHub App authentication was rejected"},
		{name: "forbidden", status: http.StatusForbidden, body: `{"id":1}`, wantReason: "GitHub App authentication was rejected"},
		{name: "rate limited", status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0"}, body: "body-secret-sentinel", wantReason: "GitHub API is unavailable"},
		{name: "primary rate limit", status: http.StatusTooManyRequests, headers: map[string]string{"X-RateLimit-Remaining": "0"}, body: "body-secret-sentinel", wantReason: "GitHub API is unavailable"},
		{name: "secondary rate limit", status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "1"}, body: "body-secret-sentinel", wantReason: "GitHub API is unavailable"},
		{name: "server error", status: http.StatusBadGateway, body: "body-secret-sentinel", wantReason: "GitHub App check returned HTTP 502"},
		{name: "malformed", status: http.StatusOK, body: "body-secret-sentinel", wantReason: "GitHub App response is invalid"},
		{name: "identity mismatch", status: http.StatusOK, body: `{"id":2}`, wantReason: "GitHub App identity does not match app_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for key, value := range tc.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			check := newDoctorCheck("main", Config{AppID: 1, PrivateKeyFile: filepath.Base(keyPath)}, filepath.Dir(keyPath), doctorOptions{
				now:     time.Now,
				client:  server.Client(),
				baseURL: server.URL,
			})
			_, failure := check.Probe(context.Background())
			if failure == nil || failure.Error() != tc.wantReason {
				t.Fatalf("failure = %v, want %q", failure, tc.wantReason)
			}
			if strings.Contains(failure.Error(), "sentinel") {
				t.Fatalf("failure leaked response body: %q", failure.Error())
			}
		})
	}
}

func TestDoctorCheckRejectsInvalidKeyConfigurationWithoutLeakingPath(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name       string
		cfg        Config
		prepare    func(t *testing.T) string
		wantReason string
	}{
		{name: "app id", cfg: Config{AppID: 0, PrivateKeyFile: "key.pem"}, wantReason: "app_id must be positive"},
		{name: "missing key", cfg: Config{AppID: 1}, wantReason: "private_key_file is required"},
		{name: "missing file", cfg: Config{AppID: 1, PrivateKeyFile: "key-secret-sentinel.pem"}, wantReason: "private key file is unreadable"},
		{name: "invalid pem", cfg: Config{AppID: 1, PrivateKeyFile: "bad.pem"}, prepare: func(t *testing.T) string {
			path := filepath.Join(dir, "bad.pem")
			if err := os.WriteFile(path, []byte("not-a-key-secret-sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, wantReason: "private key is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				tc.prepare(t)
			}
			check := NewDoctorCheck("main", tc.cfg, dir)
			_, failure := check.Probe(context.Background())
			if failure == nil || failure.Error() != tc.wantReason {
				t.Fatalf("failure = %v, want %q", failure, tc.wantReason)
			}
			if strings.Contains(failure.Error(), "secret-sentinel") || strings.Contains(failure.Error(), dir) {
				t.Fatalf("failure leaked sensitive input: %q", failure.Error())
			}
		})
	}
}

func TestDoctorCheckRejectsBroadPermissionsAndOversizedResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission check")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := writePrivateKey(t, dir, key, "pkcs1")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	check := NewDoctorCheck("main", Config{AppID: 1, PrivateKeyFile: filepath.Base(path)}, dir)
	if _, failure := check.Probe(context.Background()); failure == nil || failure.Error() != "private key file permissions are too broad" {
		t.Fatalf("permissions failure = %v", failure)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxAPIResponseSize+1))
	}))
	defer server.Close()
	check = newDoctorCheck("main", Config{AppID: 1, PrivateKeyFile: filepath.Base(path)}, dir, doctorOptions{
		now:     time.Now,
		client:  server.Client(),
		baseURL: server.URL,
	})
	if _, failure := check.Probe(context.Background()); failure == nil || failure.Error() != "GitHub App response is invalid" {
		t.Fatalf("oversized response failure = %v", failure)
	}
}

func TestDoctorCheckDisablesRedirectsAndMapsTimeout(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := writePrivateKey(t, t.TempDir(), key, "pkcs1")
	redirects := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects++
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	defer server.Close()
	check := newDoctorCheck("main", Config{AppID: 1, PrivateKeyFile: filepath.Base(path)}, filepath.Dir(path), doctorOptions{
		now:     time.Now,
		client:  server.Client(),
		baseURL: server.URL,
	})
	if _, failure := check.Probe(context.Background()); failure == nil || failure.Error() != "GitHub App check returned HTTP 302" {
		t.Fatalf("redirect failure = %v", failure)
	}
	if redirects != 1 {
		t.Fatalf("redirect requests = %d, want 1", redirects)
	}

	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hanging.Close()
	client := hanging.Client()
	client.Timeout = 20 * time.Millisecond
	check = newDoctorCheck("main", Config{AppID: 1, PrivateKeyFile: filepath.Base(path)}, filepath.Dir(path), doctorOptions{
		now:     time.Now,
		client:  client,
		baseURL: hanging.URL,
	})
	if _, failure := check.Probe(context.Background()); failure == nil || failure.Error() != "GitHub API is unavailable" {
		t.Fatalf("timeout failure = %v", failure)
	}
}

func verifyJWT(t *testing.T, token string, publicKey *rsa.PublicKey, now time.Time, appID int64) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT parts = %d", len(parts))
	}
	decode := func(part string) []byte {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Algorithm != "RS256" || header.Type != "JWT" {
		t.Fatalf("header = %+v", header)
	}
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.IssuedAt != now.Unix()-60 || claims.ExpiresAt != now.Unix()+9*60 || claims.Issuer != strconv.FormatInt(appID, 10) {
		t.Fatalf("claims = %+v", claims)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature := decode(parts[2])
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("JWT signature: %v", err)
	}
}

func writePrivateKey(t *testing.T, dir string, key *rsa.PrivateKey, format string) string {
	t.Helper()
	var data []byte
	var blockType string
	switch format {
	case "pkcs1":
		data = x509.MarshalPKCS1PrivateKey(key)
		blockType = "RSA PRIVATE KEY"
	case "pkcs8":
		var err error
		data, err = x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		blockType = "PRIVATE KEY"
	default:
		t.Fatalf("unknown key format %q", format)
	}
	path := filepath.Join(dir, format+".pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
