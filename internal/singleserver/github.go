package singleserver

import (
	"bytes"
	"crypto"
	"crypto/hmac"
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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const githubAPI = "https://api.github.com"

type GitHubAppSecrets struct {
	AppID         int64  `json:"app_id"`
	Slug          string `json:"slug"`
	WebhookSecret string `json:"webhook_secret"`
}

type InstallationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type StatusRequest struct {
	State       string `json:"state"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

type RepositoryInstallation struct {
	ID int64 `json:"id"`
}

type RepositoryResponse struct {
	DefaultBranch string `json:"default_branch"`
}

type CommitResponse struct {
	SHA string `json:"sha"`
}

type RepositoryContentResponse struct {
	Type string `json:"type"`
}

type GitHubHookConfig struct {
	URL string `json:"url"`
}

type GitHubClient struct {
	httpClient *http.Client
	stateDir   string
	tokenMu    sync.Mutex
	tokenCache map[int64]InstallationToken
}

func NewGitHubClient(stateDir string) *GitHubClient {
	if stateDir == "" {
		stateDir = "/etc/singleserver"
	}
	return &GitHubClient{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		stateDir:   stateDir,
		tokenCache: map[int64]InstallationToken{},
	}
}

func VerifyWebhookSignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + fmt.Sprintf("%x", mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature)))
}

func (c *GitHubClient) WebhookSecret() (string, error) {
	webhookSecretFromEnv := strings.TrimSpace(os.Getenv("SINGLESERVER_WEBHOOK_SECRET"))
	if webhookSecretFromEnv != "" {
		return webhookSecretFromEnv, nil
	}

	secrets, err := c.LoadSecrets()
	if err != nil {
		return "", err
	}
	if secrets.WebhookSecret == "" {
		return "", errors.New("webhook secret is missing")
	}
	return secrets.WebhookSecret, nil
}

func (c *GitHubClient) LoadSecrets() (*GitHubAppSecrets, error) {
	appIDFromEnv := strings.TrimSpace(os.Getenv("SINGLESERVER_GITHUB_APP_ID"))
	webhookSecretFromEnv := strings.TrimSpace(os.Getenv("SINGLESERVER_WEBHOOK_SECRET"))
	if appIDFromEnv != "" && webhookSecretFromEnv != "" {
		appID, err := strconv.ParseInt(appIDFromEnv, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid SINGLESERVER_GITHUB_APP_ID: %w", err)
		}
		return &GitHubAppSecrets{AppID: appID, WebhookSecret: webhookSecretFromEnv}, nil
	}

	body, err := os.ReadFile(filepath.Join(c.stateDir, "github-app.json"))
	if err != nil {
		return nil, err
	}
	var secrets GitHubAppSecrets
	if err := json.Unmarshal(body, &secrets); err != nil {
		return nil, err
	}
	if secrets.AppID == 0 || secrets.WebhookSecret == "" {
		return nil, errors.New("github-app.json is missing app_id or webhook_secret")
	}
	return &secrets, nil
}

func (c *GitHubClient) DeployToken(installationID int64) (string, error) {
	if installationID == 0 {
		return "", errors.New("missing installation id")
	}
	return c.InstallationToken(installationID)
}

func (c *GitHubClient) InstallationToken(installationID int64) (string, error) {
	c.tokenMu.Lock()
	cached, ok := c.tokenCache[installationID]
	if ok && time.Until(cached.ExpiresAt) > time.Minute {
		c.tokenMu.Unlock()
		return cached.Token, nil
	}
	c.tokenMu.Unlock()

	jwt, err := c.AppJWT()
	if err != nil {
		return "", err
	}
	var token InstallationToken
	if err := c.request("POST", fmt.Sprintf("/app/installations/%d/access_tokens", installationID), "Bearer "+jwt, nil, &token); err != nil {
		return "", err
	}

	c.tokenMu.Lock()
	c.tokenCache[installationID] = token
	c.tokenMu.Unlock()
	return token.Token, nil
}

func (c *GitHubClient) AppJWT() (string, error) {
	secrets, err := c.LoadSecrets()
	if err != nil {
		return "", err
	}
	privateKey, err := c.loadPrivateKey()
	if err != nil {
		return "", err
	}

	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": secrets.AppID,
	}
	unsigned := base64URLJSON(header) + "." + base64URLJSON(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *GitHubClient) CreateCommitStatus(repo string, sha string, token string, state string, description string) error {
	if len(description) > 140 {
		description = description[:140]
	}
	body := StatusRequest{
		State:       state,
		Description: description,
		Context:     "Single Server",
	}
	return c.request("POST", fmt.Sprintf("/repos/%s/statuses/%s", repo, sha), "Bearer "+token, body, nil)
}

func (c *GitHubClient) RepositoryInstallationID(repo string) (int64, error) {
	jwt, err := c.AppJWT()
	if err != nil {
		return 0, err
	}
	var installation RepositoryInstallation
	if err := c.request("GET", fmt.Sprintf("/repos/%s/installation", repo), "Bearer "+jwt, nil, &installation); err != nil {
		return 0, err
	}
	if installation.ID == 0 {
		return 0, errors.New("repository installation id is missing")
	}
	return installation.ID, nil
}

func (c *GitHubClient) HookConfig() (*GitHubHookConfig, error) {
	jwt, err := c.AppJWT()
	if err != nil {
		return nil, err
	}
	var config GitHubHookConfig
	if err := c.request("GET", "/app/hook/config", "Bearer "+jwt, nil, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *GitHubClient) RepositoryDefaultBranch(repo string, token string) (string, error) {
	var repository RepositoryResponse
	if err := c.request("GET", fmt.Sprintf("/repos/%s", repo), "Bearer "+token, nil, &repository); err != nil {
		return "", err
	}
	if repository.DefaultBranch == "" {
		return "", errors.New("repository default branch is missing")
	}
	return repository.DefaultBranch, nil
}

func (c *GitHubClient) CommitSHA(repo string, ref string, token string) (string, error) {
	if ref == "" {
		return "", errors.New("ref is required")
	}
	var commit CommitResponse
	if err := c.request("GET", fmt.Sprintf("/repos/%s/commits/%s", repo, ref), "Bearer "+token, nil, &commit); err != nil {
		return "", err
	}
	if commit.SHA == "" {
		return "", errors.New("commit sha is missing")
	}
	return commit.SHA, nil
}

func (c *GitHubClient) RepositoryFileExists(repo string, path string, ref string, token string) (bool, error) {
	if path == "" {
		return false, errors.New("path is required")
	}
	if ref == "" {
		return false, errors.New("ref is required")
	}
	var content RepositoryContentResponse
	err := c.request("GET", fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repo, escapeContentPath(path), url.QueryEscape(ref)), "Bearer "+token, nil, &content)
	if err != nil {
		if strings.Contains(err.Error(), "Not Found") {
			return false, nil
		}
		return false, err
	}
	return content.Type == "file", nil
}

func escapeContentPath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c *GitHubClient) ConvertManifestCode(code string) (*GitHubAppSecrets, string, error) {
	var response struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		HTMLURL       string `json:"html_url"`
	}
	if err := c.request("POST", "/app-manifests/"+code+"/conversions", "", nil, &response); err != nil {
		return nil, "", err
	}
	if response.ID == 0 || response.WebhookSecret == "" || response.PEM == "" {
		return nil, "", errors.New("manifest conversion response was missing app credentials")
	}

	if err := os.MkdirAll(c.stateDir, 0700); err != nil {
		return nil, "", err
	}
	privateKeyPath := filepath.Join(c.stateDir, "github-app.private-key.pem")
	if err := os.WriteFile(privateKeyPath, []byte(response.PEM), 0600); err != nil {
		return nil, "", err
	}

	secrets := &GitHubAppSecrets{
		AppID:         response.ID,
		Slug:          response.Slug,
		WebhookSecret: response.WebhookSecret,
	}
	body, _ := json.MarshalIndent(secrets, "", "  ")
	if err := os.WriteFile(filepath.Join(c.stateDir, "github-app.json"), append(body, '\n'), 0600); err != nil {
		return nil, "", err
	}

	installURL := response.HTMLURL
	if response.Slug != "" {
		installURL = "https://github.com/apps/" + response.Slug + "/installations/new"
	}
	return secrets, installURL, nil
}

func (c *GitHubClient) loadPrivateKey() (*rsa.PrivateKey, error) {
	privateKeyEnv := strings.TrimSpace(os.Getenv("SINGLESERVER_GITHUB_PRIVATE_KEY"))
	var pemBytes []byte
	if privateKeyEnv != "" {
		pemBytes = []byte(strings.ReplaceAll(privateKeyEnv, `\n`, "\n"))
	} else {
		privateKeyPath := strings.TrimSpace(os.Getenv("SINGLESERVER_GITHUB_PRIVATE_KEY_PATH"))
		if privateKeyPath == "" {
			privateKeyPath = filepath.Join(c.stateDir, "github-app.private-key.pem")
		}
		body, err := os.ReadFile(privateKeyPath)
		if err != nil {
			return nil, err
		}
		pemBytes = body
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("failed to decode private key PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}

func (c *GitHubClient) request(method string, path string, authorization string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, githubAPI+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "singleserver")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var apiError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(resBody, &apiError)
		if apiError.Message == "" {
			apiError.Message = string(resBody)
		}
		return fmt.Errorf("GitHub API %s %s failed: %s", method, path, apiError.Message)
	}
	if output != nil && len(resBody) > 0 {
		return json.Unmarshal(resBody, output)
	}
	return nil
}

func base64URLJSON(value any) string {
	body, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(body)
}
