package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VaalaCat/ai-gateway/internal/consts"
	"github.com/VaalaCat/ai-gateway/internal/version"
)

const (
	DefaultBaseURL = "https://api.githubcopilot.com"

	OAuthAppOpenCode   = "opencode"
	OAuthAppCopilotAPI = "copilot-api"

	HeaderInitiator     = "x-initiator"
	HeaderOpenAIIntent  = "Openai-Intent"
	HeaderVisionRequest = "Copilot-Vision-Request"

	openAIIntentConversationEdits = "conversation-edits"
	openCodeOAuthClientID         = "Ov23li8tweQw6odWQebz"
)

var gptMajorPattern = regexp.MustCompile(`^gpt-(\d+)`)

type DeviceStartRequest struct {
	ClientID      string
	EnterpriseURL string
	HTTPClient    *http.Client
}

type DeviceStartResponse struct {
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	BaseURL         string `json:"base_url"`
	EnterpriseURL   string `json:"enterprise_url,omitempty"`
}

type DevicePollRequest struct {
	ClientID      string
	DeviceCode    string
	EnterpriseURL string
	HTTPClient    *http.Client
}

type DevicePollResponse struct {
	Status      string `json:"status"`
	AccessToken string `json:"access_token,omitempty"`
	Interval    int    `json:"interval,omitempty"`
	Error       string `json:"error,omitempty"`
}

func NormalizeEnterpriseDomain(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid enterprise URL")
	}
	return u.Hostname(), nil
}

func BaseURL(enterpriseDomain string) string {
	if strings.TrimSpace(enterpriseDomain) == "" || strings.TrimSpace(enterpriseDomain) == "github.com" {
		return DefaultBaseURL
	}
	return "https://copilot-api." + strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(enterpriseDomain, "https://"), "http://"), "/")
}

func DeviceURLs(enterpriseDomain string) (deviceCodeURL, accessTokenURL string) {
	domain := strings.TrimSpace(enterpriseDomain)
	if domain == "" {
		domain = "github.com"
	}
	return "https://" + domain + "/login/device/code", "https://" + domain + "/login/oauth/access_token"
}

func ResolveOAuthClientID(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case OAuthAppOpenCode, OAuthAppCopilotAPI:
		return openCodeOAuthClientID
	default:
		return value
	}
}

func CopilotTokenURL(enterpriseDomain string) string {
	domain := strings.TrimSpace(enterpriseDomain)
	if domain == "" || domain == "github.com" {
		return "https://api.github.com/copilot_internal/v2/token"
	}
	return "https://api." + domain + "/copilot_internal/v2/token"
}

func StartDeviceFlow(ctx context.Context, req DeviceStartRequest) (DeviceStartResponse, error) {
	clientID := ResolveOAuthClientID(req.ClientID)
	if clientID == "" {
		return DeviceStartResponse{}, fmt.Errorf("github copilot OAuth client id is not configured")
	}
	domain, err := NormalizeEnterpriseDomain(req.EnterpriseURL)
	if err != nil {
		return DeviceStartResponse{}, err
	}

	deviceCodeURL, _ := DeviceURLs(domain)
	body, _ := json.Marshal(map[string]string{
		"client_id": clientID,
		"scope":     "read:user",
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceCodeURL, bytes.NewReader(body))
	if err != nil {
		return DeviceStartResponse{}, err
	}
	setGitHubAuthHeaders(httpReq)

	client := req.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return DeviceStartResponse{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeviceStartResponse{}, fmt.Errorf("github device authorization failed: %d: %s", resp.StatusCode, string(data))
	}

	var parsed struct {
		VerificationURI string `json:"verification_uri"`
		UserCode        string `json:"user_code"`
		DeviceCode      string `json:"device_code"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return DeviceStartResponse{}, fmt.Errorf("parse github device response: %w", err)
	}
	if parsed.VerificationURI == "" || parsed.UserCode == "" || parsed.DeviceCode == "" {
		return DeviceStartResponse{}, fmt.Errorf("github device response missing required fields")
	}
	if parsed.Interval <= 0 {
		parsed.Interval = 5
	}

	return DeviceStartResponse{
		VerificationURI: parsed.VerificationURI,
		UserCode:        parsed.UserCode,
		DeviceCode:      parsed.DeviceCode,
		Interval:        parsed.Interval,
		BaseURL:         BaseURL(domain),
		EnterpriseURL:   domain,
	}, nil
}

func PollDeviceFlow(ctx context.Context, req DevicePollRequest) (DevicePollResponse, error) {
	clientID := ResolveOAuthClientID(req.ClientID)
	if clientID == "" {
		return DevicePollResponse{}, fmt.Errorf("github copilot OAuth client id is not configured")
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		return DevicePollResponse{}, fmt.Errorf("device_code is required")
	}
	domain, err := NormalizeEnterpriseDomain(req.EnterpriseURL)
	if err != nil {
		return DevicePollResponse{}, err
	}

	_, accessTokenURL := DeviceURLs(domain)
	body, _ := json.Marshal(map[string]string{
		"client_id":   clientID,
		"device_code": req.DeviceCode,
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, accessTokenURL, bytes.NewReader(body))
	if err != nil {
		return DevicePollResponse{}, err
	}
	setGitHubAuthHeaders(httpReq)

	client := req.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return DevicePollResponse{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DevicePollResponse{}, fmt.Errorf("github token poll failed: %d: %s", resp.StatusCode, string(data))
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Interval    int    `json:"interval"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return DevicePollResponse{}, fmt.Errorf("parse github token response: %w", err)
	}
	if parsed.AccessToken != "" {
		copilotToken, err := ExchangeGitHubToken(ctx, client, domain, parsed.AccessToken)
		if err != nil {
			return DevicePollResponse{}, err
		}
		return DevicePollResponse{Status: "success", AccessToken: copilotToken}, nil
	}
	switch parsed.Error {
	case "authorization_pending":
		return DevicePollResponse{Status: "pending", Interval: parsed.Interval}, nil
	case "slow_down":
		return DevicePollResponse{Status: "slow_down", Interval: parsed.Interval}, nil
	case "":
		return DevicePollResponse{Status: "pending", Interval: parsed.Interval}, nil
	default:
		return DevicePollResponse{Status: "failed", Error: parsed.Error}, nil
	}
}

func ExchangeGitHubToken(ctx context.Context, client *http.Client, enterpriseDomain, githubToken string) (string, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return "", fmt.Errorf("github token is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL(enterpriseDomain), nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set(consts.HeaderAuthorization, consts.BearerPrefix+githubToken)
	httpReq.Header.Set("User-Agent", userAgent())

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github copilot token exchange failed: %d: %s", resp.StatusCode, string(data))
	}

	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("parse github copilot token response: %w", err)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return "", fmt.Errorf("github copilot token response missing token")
	}
	return parsed.Token, nil
}

func setGitHubAuthHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set(consts.HeaderContentType, consts.ContentTypeJSON)
	req.Header.Set("User-Agent", userAgent())
}

type modelListResponse struct {
	Data []struct {
		ModelPickerEnabled bool   `json:"model_picker_enabled"`
		ID                 string `json:"id"`
		Policy             *struct {
			State string `json:"state"`
		} `json:"policy"`
	} `json:"data"`
}

func FetchModels(ctx context.Context, client *http.Client, baseURL, token string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("github copilot token is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	ApplyHeaders(httpReq, nil, token)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, string(body))
	}

	var parsed modelListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse response")
	}
	models := make([]string, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if item.ID == "" || !item.ModelPickerEnabled {
			continue
		}
		if item.Policy != nil && item.Policy.State == "disabled" {
			continue
		}
		models = append(models, item.ID)
	}
	sort.Strings(models)
	return models, nil
}

func ApplyHeaders(req *http.Request, body []byte, token string) {
	req.Header.Del(consts.HeaderXAPIKey)
	req.Header.Del("authorization")
	req.Header.Set(consts.HeaderAuthorization, consts.BearerPrefix+token)
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set(HeaderInitiator, "user")
	req.Header.Set(HeaderOpenAIIntent, openAIIntentConversationEdits)
	if HasVisionInput(body) {
		req.Header.Set(HeaderVisionRequest, "true")
	} else {
		req.Header.Del(HeaderVisionRequest)
	}
}

func HasVisionInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	return hasVisionValue(v)
}

func hasVisionValue(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); typ == "image_url" || typ == "input_image" || typ == "image" {
			return true
		}
		for _, child := range x {
			if hasVisionValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if hasVisionValue(child) {
				return true
			}
		}
	}
	return false
}

func ProtocolOverride(model string) map[string]string {
	if strings.HasPrefix(model, "claude") {
		return map[string]string{
			"openai_chat":      "claude",
			"openai_responses": "claude",
			"claude":           "claude",
		}
	}
	match := gptMajorPattern.FindStringSubmatch(model)
	major := 0
	if len(match) == 2 {
		major, _ = strconv.Atoi(match[1])
	}
	if major >= 5 && !strings.HasPrefix(model, "gpt-5-mini") {
		return map[string]string{
			"openai_chat":      "openai_responses",
			"openai_responses": "openai_responses",
			"claude":           "openai_responses",
		}
	}
	return nil
}

func userAgent() string {
	return "vaala-ai-gateway/" + version.Version
}
