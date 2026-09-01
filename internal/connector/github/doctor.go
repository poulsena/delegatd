package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/poulsena/delegatd/internal/doctor"
)

const (
	defaultAPIBaseURL  = "https://api.github.com"
	maxPrivateKeySize  = 64 << 10
	maxAPIResponseSize = 1 << 20
)

// Config is the adapter-owned GitHub App authentication configuration.
type Config struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
}

type doctorOptions struct {
	now     func() time.Time
	client  *http.Client
	baseURL string
}

// NewDoctorCheck constructs the read-only GitHub App diagnosis.
func NewDoctorCheck(name string, cfg Config, dir string) doctor.Check {
	return newDoctorCheck(name, cfg, dir, doctorOptions{
		now:     time.Now,
		client:  http.DefaultClient,
		baseURL: defaultAPIBaseURL,
	})
}

// newDoctorCheck keeps the clock, HTTP transport, and endpoint injectable for
// deterministic adapter tests without changing the application-facing port.
func newDoctorCheck(name string, cfg Config, dir string, options doctorOptions) doctor.Check {
	if options.now == nil {
		options.now = time.Now
	}
	if options.client == nil {
		options.client = http.DefaultClient
	}
	if options.baseURL == "" {
		options.baseURL = defaultAPIBaseURL
	}
	return doctor.Check{
		ID: "connector." + name,
		Probe: func(ctx context.Context) (string, *doctor.Failure) {
			return probe(ctx, cfg, dir, options)
		},
	}
}

func probe(ctx context.Context, cfg Config, dir string, options doctorOptions) (string, *doctor.Failure) {
	if cfg.AppID <= 0 {
		return "", doctor.NewFailure("app_id must be positive", nil)
	}
	if cfg.PrivateKeyFile == "" {
		return "", doctor.NewFailure("private_key_file is required", nil)
	}

	keyPath := cfg.PrivateKeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(dir, keyPath)
	}
	info, err := os.Stat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPrivateKeySize {
		if err == nil {
			err = errors.New("private key file is not a usable regular file")
		}
		return "", doctor.NewFailure("private key file is unreadable", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", doctor.NewFailure("private key file permissions are too broad", nil)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil || len(keyBytes) > maxPrivateKeySize {
		return "", doctor.NewFailure("private key file is unreadable", err)
	}
	key, err := parseRSAKey(keyBytes)
	if err != nil {
		return "", doctor.NewFailure("private key is invalid", err)
	}

	jwt, err := appJWT(key, cfg.AppID, options.now())
	if err != nil {
		return "", doctor.NewFailure("private key is invalid", err)
	}
	endpoint := strings.TrimRight(options.baseURL, "/") + "/app"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", doctor.NewFailure("GitHub API is unavailable", err)
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "delegatd/doctor")

	client := *options.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return "", doctor.NewFailure("GitHub API is unavailable", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && githubRateLimited(response)) {
		return "", doctor.NewFailure("GitHub API is unavailable", nil)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "", doctor.NewFailure("GitHub App authentication was rejected", nil)
	}
	if response.StatusCode != http.StatusOK {
		return "", doctor.NewFailure(fmt.Sprintf("GitHub App check returned HTTP %d", response.StatusCode), nil)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseSize+1))
	if err != nil || len(body) > maxAPIResponseSize {
		return "", doctor.NewFailure("GitHub App response is invalid", err)
	}
	var identity struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &identity); err != nil || identity.ID == nil {
		return "", doctor.NewFailure("GitHub App response is invalid", err)
	}
	if *identity.ID != cfg.AppID {
		return "", doctor.NewFailure("GitHub App identity does not match app_id", nil)
	}
	return "GitHub App authenticated", nil
}

func githubRateLimited(response *http.Response) bool {
	return strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining")) == "0" ||
		strings.TrimSpace(response.Header.Get("Retry-After")) != ""
}

func parseRSAKey(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid PEM")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not RSA")
		}
		return key, nil
	default:
		return nil, errors.New("unsupported private key PEM type")
	}
}

func appJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	type header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	type claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	encode := func(value any) (string, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(data), nil
	}
	headerPart, err := encode(header{Algorithm: "RS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claimsPart, err := encode(claims{
		IssuedAt:  now.Unix() - 60,
		ExpiresAt: now.Unix() + 9*60,
		Issuer:    strconv.FormatInt(appID, 10),
	})
	if err != nil {
		return "", err
	}
	unsigned := headerPart + "." + claimsPart
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
