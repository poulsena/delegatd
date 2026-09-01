package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/poulsena/delegatd/internal/config"
	"github.com/poulsena/delegatd/internal/domain"
)

const (
	defaultRepositoryTotalTimeout   = 30 * time.Second
	defaultRepositoryRequestTimeout = 10 * time.Second
)

var (
	githubOwnerPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	commitPattern           = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
)
var errResponseTooLarge = errors.New("repository response exceeded limit")

// RepositoryConfig is the adapter-owned logical repository configuration.
type RepositoryConfig struct {
	ExternalRef string `yaml:"external_ref"`
}

type repositoryOptions struct {
	now            func() time.Time
	client         *http.Client
	baseURL        string
	totalTimeout   time.Duration
	requestTimeout time.Duration
}

type RepositorySource struct {
	app      Config
	resource RepositoryConfig
	dir      string
	owner    string
	repo     string
	options  repositoryOptions
}

// NewRepositorySource constructs the trusted, read-only GitHub repository
// source used by manual task submission.
func NewRepositorySource(app Config, resource RepositoryConfig, dir string) (*RepositorySource, error) {
	return newRepositorySource(app, resource, dir, repositoryOptions{
		now:            time.Now,
		client:         http.DefaultClient,
		baseURL:        defaultAPIBaseURL,
		totalTimeout:   defaultRepositoryTotalTimeout,
		requestTimeout: defaultRepositoryRequestTimeout,
	})
}

func newRepositorySource(app Config, resource RepositoryConfig, dir string, options repositoryOptions) (*RepositorySource, error) {
	owner, repo, err := parseExternalRef(resource.ExternalRef)
	if err != nil {
		return nil, err
	}
	if options.now == nil {
		options.now = time.Now
	}
	if options.client == nil {
		options.client = http.DefaultClient
	}
	if options.baseURL == "" {
		options.baseURL = defaultAPIBaseURL
	}
	if options.totalTimeout <= 0 {
		options.totalTimeout = defaultRepositoryTotalTimeout
	}
	if options.requestTimeout <= 0 {
		options.requestTimeout = defaultRepositoryRequestTimeout
	}
	return &RepositorySource{
		app:      app,
		resource: resource,
		dir:      dir,
		owner:    owner,
		repo:     repo,
		options:  options,
	}, nil
}

func parseExternalRef(value string) (string, string, error) {
	if value != strings.TrimSpace(value) {
		return "", "", errors.New("invalid external_ref")
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !githubOwnerPattern.MatchString(parts[0]) || !githubRepositoryPattern.MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." || strings.HasSuffix(strings.ToLower(parts[1]), ".git") {
		return "", "", errors.New("invalid external_ref")
	}
	return parts[0], parts[1], nil
}

type repositoryError struct {
	reason string
	cause  error
}

func (e *repositoryError) Error() string {
	if e == nil {
		return ""
	}
	return e.reason
}

func (e *repositoryError) SafeReason() string {
	if e == nil {
		return ""
	}
	return e.reason
}

func (e *repositoryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newRepositoryError(reason string, cause error) error {
	return &repositoryError{reason: reason, cause: cause}
}

func (s *RepositorySource) Snapshot(ctx context.Context) (domain.RepositoryMaterial, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return domain.RepositoryMaterial{}, newRepositoryError("task cancelled", err)
	}
	snapshotContext, cancel := context.WithTimeout(ctx, s.options.totalTimeout)
	defer cancel()

	key, reason, err := loadPrivateKey(s.app, s.dir)
	if reason != "" {
		return domain.RepositoryMaterial{}, newRepositoryError(reason, err)
	}
	jwt, err := appJWT(key, s.app.AppID, s.options.now())
	if err != nil {
		return domain.RepositoryMaterial{}, newRepositoryError("private key is invalid", err)
	}

	installation, err := s.request(snapshotContext, http.MethodGet, s.repositoryPath()+"/installation", nil, jwt, "application/vnd.github+json", nil)
	if err != nil {
		return domain.RepositoryMaterial{}, s.mapError(ctx, snapshotContext, err)
	}
	if installation.status == http.StatusNotFound {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is not installed", nil)
	}
	if installation.status != http.StatusOK {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", nil)
	}
	var installationIdentity struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(installation.body, &installationIdentity); err != nil || installationIdentity.ID == nil || *installationIdentity.ID <= 0 {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", err)
	}

	tokenRequest := struct {
		Repositories []string `json:"repositories"`
		Permissions  struct {
			Contents string `json:"contents"`
			Metadata string `json:"metadata"`
		} `json:"permissions"`
	}{Repositories: []string{s.repo}}
	tokenRequest.Permissions.Contents = "read"
	tokenRequest.Permissions.Metadata = "read"
	tokenBody, err := json.Marshal(tokenRequest)
	if err != nil {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", err)
	}
	tokenEndpoint := "/app/installations/" + strconv.FormatInt(*installationIdentity.ID, 10) + "/access_tokens"
	tokenResponse, err := s.request(snapshotContext, http.MethodPost, tokenEndpoint, nil, jwt, "application/vnd.github+json", tokenBody)
	if err != nil {
		return domain.RepositoryMaterial{}, s.mapError(ctx, snapshotContext, err)
	}
	if tokenResponse.status != http.StatusCreated {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", nil)
	}
	var installationToken struct {
		Token       string            `json:"token"`
		ExpiresAt   string            `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal(tokenResponse.body, &installationToken); err != nil {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, installationToken.ExpiresAt)
	if err != nil || installationToken.Token == "" || !expiresAt.After(s.options.now()) || len(installationToken.Permissions) != 2 || installationToken.Permissions["contents"] != "read" || installationToken.Permissions["metadata"] != "read" {
		if err == nil {
			err = errors.New("installation token was empty, expired, or over-scoped")
		}
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", err)
	}

	repositoryResponse, err := s.request(snapshotContext, http.MethodGet, s.repositoryPath(), nil, installationToken.Token, "application/vnd.github+json", nil)
	if err != nil {
		return domain.RepositoryMaterial{}, s.mapError(ctx, snapshotContext, err)
	}
	if repositoryResponse.status != http.StatusOK {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", nil)
	}
	var repositoryIdentity struct {
		ID            *int64  `json:"id"`
		FullName      *string `json:"full_name"`
		DefaultBranch *string `json:"default_branch"`
	}
	if err := json.Unmarshal(repositoryResponse.body, &repositoryIdentity); err != nil || repositoryIdentity.ID == nil || *repositoryIdentity.ID <= 0 || repositoryIdentity.FullName == nil || repositoryIdentity.DefaultBranch == nil || *repositoryIdentity.DefaultBranch == "" || !strings.EqualFold(*repositoryIdentity.FullName, s.resource.ExternalRef) {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", err)
	}

	commitQuery := url.Values{}
	commitQuery.Set("sha", *repositoryIdentity.DefaultBranch)
	commitQuery.Set("per_page", "1")
	commits, err := s.request(snapshotContext, http.MethodGet, s.repositoryPath()+"/commits", commitQuery, installationToken.Token, "application/vnd.github+json", nil)
	if err != nil {
		return domain.RepositoryMaterial{}, s.mapError(ctx, snapshotContext, err)
	}
	if commits.status != http.StatusOK {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", nil)
	}
	var commitList []struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(commits.body, &commitList); err != nil || len(commitList) == 0 || !commitPattern.MatchString(commitList[0].SHA) {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", err)
	}
	commitSHA := strings.ToLower(commitList[0].SHA)

	configQuery := url.Values{}
	configQuery.Set("ref", commitSHA)
	configurationResponse, err := s.request(snapshotContext, http.MethodGet, s.repositoryPath()+"/contents/.delegatd.yaml", configQuery, installationToken.Token, "application/vnd.github.raw+json", nil)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return domain.RepositoryMaterial{}, newRepositoryError("repository configuration is invalid", err)
		}
		return domain.RepositoryMaterial{}, s.mapError(ctx, snapshotContext, err)
	}
	var configuration domain.RepositoryConfiguration
	if configurationResponse.status == http.StatusNotFound {
		configuration = emptyRepositoryConfiguration()
	} else if configurationResponse.status != http.StatusOK {
		return domain.RepositoryMaterial{}, newRepositoryError("repository is unavailable", nil)
	} else {
		configuration, err = config.DecodeRepository(configurationResponse.body)
		if err != nil {
			return domain.RepositoryMaterial{}, newRepositoryError("repository configuration is invalid", err)
		}
	}
	return domain.RepositoryMaterial{
		ExternalRef:      *repositoryIdentity.FullName,
		ExternalIdentity: strconv.FormatInt(*repositoryIdentity.ID, 10),
		Revision:         commitSHA,
		Configuration:    configuration,
	}, nil
}

func emptyRepositoryConfiguration() domain.RepositoryConfiguration {
	return domain.RepositoryConfiguration{
		Version:    1,
		Agent:      domain.AgentConfiguration{Instructions: []string{}},
		Policy:     domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}},
		Validation: domain.ValidationConfiguration{Required: []domain.ValidationCommand{}},
	}
}

type apiResponse struct {
	status int
	body   []byte
}

func (s *RepositorySource) request(ctx context.Context, method, pathValue string, query url.Values, credential, accept string, body []byte) (apiResponse, error) {
	endpoint, err := s.endpoint(pathValue, query)
	if err != nil {
		return apiResponse{}, newRepositoryError("repository is unavailable", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return apiResponse{}, newRepositoryError("repository is unavailable", err)
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "delegatd/task")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := *s.options.client
	if client.Timeout == 0 || client.Timeout > s.options.requestTimeout {
		client.Timeout = s.options.requestTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return apiResponse{}, newRepositoryError("repository is unavailable", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseSize+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		if readErr != nil {
			return apiResponse{}, newRepositoryError("repository is unavailable", readErr)
		}
		return apiResponse{}, newRepositoryError("repository is unavailable", closeErr)
	}
	if len(responseBody) > maxAPIResponseSize {
		return apiResponse{}, newRepositoryError("repository is unavailable", errResponseTooLarge)
	}
	return apiResponse{status: response.StatusCode, body: responseBody}, nil
}

func (s *RepositorySource) endpoint(pathValue string, query url.Values) (string, error) {
	base, err := url.Parse(s.options.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid GitHub API base URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + pathValue
	base.RawPath = ""
	base.RawQuery = query.Encode()
	base.Fragment = ""
	return base.String(), nil
}

func (s *RepositorySource) repositoryPath() string {
	return "/repos/" + url.PathEscape(s.owner) + "/" + url.PathEscape(s.repo)
}

func (s *RepositorySource) mapError(parent, child context.Context, err error) error {
	if parent.Err() != nil {
		return newRepositoryError("task cancelled", parent.Err())
	}
	if child.Err() != nil {
		return newRepositoryError("repository is unavailable", child.Err())
	}
	var sourceErr *repositoryError
	if errors.As(err, &sourceErr) {
		return sourceErr
	}
	return newRepositoryError("repository is unavailable", err)
}
