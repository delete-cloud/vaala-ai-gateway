package copilot

import (
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
	"sync"
	"time"

	"github.com/VaalaCat/ai-gateway/internal/consts"
	"github.com/VaalaCat/ai-gateway/internal/version"
)

const (
	DefaultBaseURL       = "https://api.githubcopilot.com"
	DefaultOAuthClientID = "Iv1.b507a08c87ecfe98"

	HeaderInitiator     = "x-initiator"
	HeaderOpenAIIntent  = "Openai-Intent"
	HeaderVisionRequest = "Copilot-Vision-Request"
	HeaderEditorVersion = "Editor-Version"
	HeaderPluginVersion = "Editor-Plugin-Version"

	openAIIntentConversationEdits = "conversation-edits"
	editorVersion                 = "vscode/1.100.0"
	editorPluginVersion           = "copilot/1.300.0"

	defaultTokenTTL   = 25 * time.Minute
	tokenExpiryBuffer = 5 * time.Minute

	maxTokenCacheEntries        = 256
	maxModelCatalogCacheEntries = 128
)

var gptMajorPattern = regexp.MustCompile(`^gpt-(\d+)`)

var apiTokenCache = struct {
	sync.Mutex
	items map[string]cachedAPIToken
}{
	items: make(map[string]cachedAPIToken),
}

var modelCatalogCache = struct {
	sync.Mutex
	items map[string]cachedModelCatalog
}{
	items: make(map[string]cachedModelCatalog),
}

type cachedAPIToken struct {
	token     string
	baseURL   string
	expiresAt time.Time
}

type cachedModelCatalog struct {
	models    []ModelInfo
	expiresAt time.Time
}

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

type APIToken struct {
	Token     string
	BaseURL   string
	ExpiresAt time.Time
}

type QuotaSnapshot struct {
	Reported         bool    `json:"reported"`
	Entitlement      float64 `json:"entitlement"`
	Remaining        float64 `json:"remaining"`
	Used             float64 `json:"used"`
	PercentRemaining float64 `json:"percent_remaining"`
	PercentUsed      float64 `json:"percent_used"`
	Unlimited        bool    `json:"unlimited"`
	OverageCount     float64 `json:"overage_count"`
	OveragePermitted bool    `json:"overage_permitted"`
	QuotaID          string  `json:"quota_id,omitempty"`
}

type Quota struct {
	PlanType      string        `json:"plan_type,omitempty"`
	QuotaResetAt  string        `json:"quota_reset_at,omitempty"`
	Premium       QuotaSnapshot `json:"premium_interactions"`
	Chat          QuotaSnapshot `json:"chat"`
	Completions   QuotaSnapshot `json:"completions"`
	LastUpdatedAt int64         `json:"last_updated_at"`
}

type ModelInfo struct {
	ID                 string  `json:"id"`
	ModelPickerEnabled bool    `json:"model_picker_enabled"`
	PremiumCost        float64 `json:"premium_cost"`
	PremiumCostKnown   bool    `json:"-"`
}

type OtherSettings struct {
	EnterpriseDomain string             `json:"copilot_enterprise_domain,omitempty"`
	ModelPrices      map[string]float64 `json:"copilot_model_prices,omitempty"`
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

func CopilotTokenURL(enterpriseDomain string) string {
	domain := strings.TrimSpace(enterpriseDomain)
	if domain == "" || domain == "github.com" {
		return "https://api.github.com/copilot_internal/v2/token"
	}
	return "https://api." + domain + "/copilot_internal/v2/token"
}

func StartDeviceFlow(ctx context.Context, req DeviceStartRequest) (DeviceStartResponse, error) {
	clientID := ResolveOAuthClientID(req.ClientID)
	domain, err := NormalizeEnterpriseDomain(req.EnterpriseURL)
	if err != nil {
		return DeviceStartResponse{}, err
	}

	deviceCodeURL, _ := DeviceURLs(domain)
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", "read:user user:email")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceCodeURL, strings.NewReader(form.Encode()))
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
	if strings.TrimSpace(req.DeviceCode) == "" {
		return DevicePollResponse{}, fmt.Errorf("device_code is required")
	}
	domain, err := NormalizeEnterpriseDomain(req.EnterpriseURL)
	if err != nil {
		return DevicePollResponse{}, err
	}

	_, accessTokenURL := DeviceURLs(domain)
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("device_code", req.DeviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, accessTokenURL, strings.NewReader(form.Encode()))
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
		if _, err := ExchangeGitHubToken(ctx, client, domain, parsed.AccessToken); err != nil {
			return DevicePollResponse{}, err
		}
		return DevicePollResponse{
			Status:      "success",
			AccessToken: parsed.AccessToken,
		}, nil
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

func ResolveOAuthClientID(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured)
	}
	return DefaultOAuthClientID
}

func EnterpriseDomainFromOtherSettings(raw string) string {
	settings := ParseOtherSettings(raw)
	return settings.EnterpriseDomain
}

func ParseOtherSettings(raw string) OtherSettings {
	var settings OtherSettings
	if strings.TrimSpace(raw) == "" {
		return settings
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return settings
	}
	if v, ok := obj["copilot_enterprise_domain"].(string); ok {
		settings.EnterpriseDomain = strings.TrimSpace(v)
	}
	if rawPrices, ok := obj["copilot_model_prices"].(map[string]any); ok {
		settings.ModelPrices = make(map[string]float64, len(rawPrices))
		for model, rawPrice := range rawPrices {
			if price, ok := numericValue(rawPrice); ok && price >= 0 {
				settings.ModelPrices[model] = price
			}
		}
	}
	return settings
}

func MergeOtherSettings(raw string, patch OtherSettings) (string, error) {
	obj := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(patch.EnterpriseDomain) != "" {
		obj["copilot_enterprise_domain"] = strings.TrimSpace(patch.EnterpriseDomain)
	}
	if len(patch.ModelPrices) > 0 {
		prices := make(map[string]float64, len(patch.ModelPrices))
		for model, price := range patch.ModelPrices {
			if strings.TrimSpace(model) != "" && price >= 0 {
				prices[strings.TrimSpace(model)] = price
			}
		}
		obj["copilot_model_prices"] = prices
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func ModelPricesFromCatalog(catalog []ModelInfo) map[string]float64 {
	prices := make(map[string]float64)
	for _, item := range catalog {
		if strings.TrimSpace(item.ID) != "" && item.PremiumCostKnown {
			prices[item.ID] = item.PremiumCost
		}
	}
	return prices
}

func PremiumCostFromOtherSettings(raw, model string) (float64, bool) {
	settings := ParseOtherSettings(raw)
	price, ok := settings.ModelPrices[model]
	if !ok || price < 0 {
		return 0, false
	}
	return price, true
}

func ExchangeGitHubToken(ctx context.Context, client *http.Client, enterpriseDomain, githubToken string) (string, error) {
	apiToken, err := GetAPIToken(ctx, client, enterpriseDomain, githubToken)
	if err != nil {
		return "", err
	}
	return apiToken.Token, nil
}

func GetAPIToken(ctx context.Context, client *http.Client, enterpriseDomain, githubToken string) (APIToken, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return APIToken{}, fmt.Errorf("github token is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	cacheKey := strings.TrimSpace(enterpriseDomain) + "\x00" + githubToken
	now := time.Now()
	apiTokenCache.Lock()
	if cached, ok := apiTokenCache.items[cacheKey]; ok && cached.expiresAt.After(now.Add(tokenExpiryBuffer)) {
		apiTokenCache.Unlock()
		return APIToken{Token: cached.token, BaseURL: cached.baseURL, ExpiresAt: cached.expiresAt}, nil
	}
	apiTokenCache.Unlock()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL(enterpriseDomain), nil)
	if err != nil {
		return APIToken{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set(consts.HeaderAuthorization, "token "+githubToken)
	httpReq.Header.Set("User-Agent", userAgent())
	httpReq.Header.Set(HeaderEditorVersion, editorVersion)
	httpReq.Header.Set(HeaderPluginVersion, editorPluginVersion)
	httpReq.Header.Set("x-github-api-version", "2025-04-01")

	resp, err := client.Do(httpReq)
	if err != nil {
		return APIToken{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APIToken{}, fmt.Errorf("github copilot token exchange failed: %d: %s", resp.StatusCode, string(data))
	}

	var parsed struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return APIToken{}, fmt.Errorf("parse github copilot token response: %w", err)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return APIToken{}, fmt.Errorf("github copilot token response missing token")
	}
	baseURL := trustedAPIBaseURL(parsed.Endpoints.API)
	if baseURL == "" {
		baseURL = BaseURL(enterpriseDomain)
	}
	expiresAt := now.Add(defaultTokenTTL)
	if parsed.ExpiresAt > 0 {
		expiresAt = time.Unix(parsed.ExpiresAt, 0)
	}

	apiTokenCache.Lock()
	pruneExpiredAPITokenCache(now)
	if len(apiTokenCache.items) >= maxTokenCacheEntries {
		deleteOneAPITokenCacheEntry()
	}
	apiTokenCache.items[cacheKey] = cachedAPIToken{token: parsed.Token, baseURL: baseURL, expiresAt: expiresAt}
	apiTokenCache.Unlock()

	return APIToken{Token: parsed.Token, BaseURL: baseURL, ExpiresAt: expiresAt}, nil
}

func setGitHubAuthHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set(consts.HeaderContentType, "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent())
}

type modelListResponse struct {
	Data []struct {
		ModelPickerEnabled bool   `json:"model_picker_enabled"`
		ID                 string `json:"id"`
		Policy             *struct {
			State string `json:"state"`
		} `json:"policy"`
		Capabilities map[string]any `json:"capabilities"`
	} `json:"data"`
}

func FetchModels(ctx context.Context, client *http.Client, baseURL, token, enterpriseDomain string) ([]string, error) {
	catalog, err := FetchModelCatalog(ctx, client, baseURL, token, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(catalog))
	for _, item := range catalog {
		models = append(models, item.ID)
	}
	sort.Strings(models)
	return models, nil
}

func FetchModelCatalog(ctx context.Context, client *http.Client, baseURL, token, enterpriseDomain string) ([]ModelInfo, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("github copilot token is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	cacheKey := strings.TrimSpace(enterpriseDomain) + "\x00" + strings.TrimSpace(baseURL) + "\x00" + strings.TrimSpace(token)
	now := time.Now()
	modelCatalogCache.Lock()
	if cached, ok := modelCatalogCache.items[cacheKey]; ok && cached.expiresAt.After(now) {
		modelCatalogCache.Unlock()
		return cloneModelInfo(cached.models), nil
	}
	modelCatalogCache.Unlock()

	apiToken, err := GetAPIToken(ctx, client, enterpriseDomain, token)
	if err != nil {
		return nil, err
	}
	if apiToken.BaseURL != "" {
		baseURL = apiToken.BaseURL
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	ApplyHeaders(httpReq, nil, apiToken.Token)

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
	catalog := make([]ModelInfo, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if item.ID == "" || !item.ModelPickerEnabled {
			continue
		}
		if item.Policy != nil && item.Policy.State == "disabled" {
			continue
		}
		premiumCost := ExtractPremiumCost(item.Capabilities)
		premiumCostKnown := premiumCost > 0
		if !premiumCostKnown {
			premiumCost, premiumCostKnown = PremiumCostForModel(item.ID)
		}
		catalog = append(catalog, ModelInfo{
			ID:                 item.ID,
			ModelPickerEnabled: item.ModelPickerEnabled,
			PremiumCost:        premiumCost,
			PremiumCostKnown:   premiumCostKnown,
		})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].ID < catalog[j].ID })
	modelCatalogCache.Lock()
	pruneExpiredModelCatalogCache(now)
	if len(modelCatalogCache.items) >= maxModelCatalogCacheEntries {
		deleteOneModelCatalogCacheEntry()
	}
	modelCatalogCache.items[cacheKey] = cachedModelCatalog{
		models:    cloneModelInfo(catalog),
		expiresAt: now.Add(10 * time.Minute),
	}
	modelCatalogCache.Unlock()
	return catalog, nil
}

func ExtractPremiumCost(capabilities map[string]any) float64 {
	if capabilities == nil {
		return 0
	}
	keys := []string{
		"premium_interactions",
		"premium_interaction_cost",
		"premium_request_cost",
		"premium_requests",
		"quota_cost",
		"request_cost",
		"cost",
		"multiplier",
	}
	for _, key := range keys {
		if value, ok := findNumericCapability(capabilities, key); ok && value > 0 {
			return value
		}
	}
	return 0
}

func PremiumCostForModel(model string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "claude-haiku-4.5":
		return 0.33, true
	case "claude-opus-4.5", "claude-opus-4.6":
		return 3, true
	case "claude-opus-4.7":
		return 15, true
	case "claude-sonnet-4.5", "claude-sonnet-4.6", "gemini-2.5-pro", "gemini-3.1-pro-preview",
		"gpt-5.2", "gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.4":
		return 1, true
	case "gemini-3-flash-preview":
		return 0.33, true
	case "gemini-3.5-flash":
		return 14, true
	case "gpt-4.1", "gpt-4o", "gpt-5-mini", "oswe-vscode-prime":
		return 0, true
	case "gpt-5.4-mini":
		return 0.33, true
	case "gpt-5.4-nano":
		return 0.25, true
	case "gpt-5.5":
		return 7.5, true
	default:
		return 0, false
	}
}

func FetchQuota(ctx context.Context, client *http.Client, enterpriseDomain, githubToken string) (Quota, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return Quota{}, fmt.Errorf("github copilot token is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotUserURL(enterpriseDomain), nil)
	if err != nil {
		return Quota{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(consts.HeaderAuthorization, "token "+githubToken)
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	req.Header.Set("x-github-api-version", "2025-04-01")

	resp, err := client.Do(req)
	if err != nil {
		return Quota{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Quota{}, fmt.Errorf("github copilot quota failed: %d: %s", resp.StatusCode, string(data))
	}

	var raw struct {
		PlanType       string `json:"copilot_plan"`
		QuotaResetDate string `json:"quota_reset_date"`
		QuotaSnapshots struct {
			PremiumInteractions rawQuotaSnapshot `json:"premium_interactions"`
			Chat                rawQuotaSnapshot `json:"chat"`
			Completions         rawQuotaSnapshot `json:"completions"`
		} `json:"quota_snapshots"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Quota{}, fmt.Errorf("parse github copilot quota response: %w", err)
	}
	return Quota{
		PlanType:      raw.PlanType,
		QuotaResetAt:  raw.QuotaResetDate,
		Premium:       normalizeQuotaSnapshot(raw.QuotaSnapshots.PremiumInteractions),
		Chat:          normalizeQuotaSnapshot(raw.QuotaSnapshots.Chat),
		Completions:   normalizeQuotaSnapshot(raw.QuotaSnapshots.Completions),
		LastUpdatedAt: time.Now().Unix(),
	}, nil
}

func CopilotUserURL(enterpriseDomain string) string {
	domain := strings.TrimSpace(enterpriseDomain)
	if domain == "" || domain == "github.com" {
		return "https://api.github.com/copilot_internal/user"
	}
	return "https://api." + domain + "/copilot_internal/user"
}

func ApplyHeaders(req *http.Request, body []byte, token string) {
	req.Header.Del(consts.HeaderXAPIKey)
	req.Header.Del("authorization")
	req.Header.Set(consts.HeaderAuthorization, consts.BearerPrefix+token)
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set(HeaderEditorVersion, editorVersion)
	req.Header.Set(HeaderPluginVersion, editorPluginVersion)
	req.Header.Set(HeaderInitiator, "user")
	req.Header.Set(HeaderOpenAIIntent, openAIIntentConversationEdits)
	if HasVisionInput(body) {
		req.Header.Set(HeaderVisionRequest, "true")
	} else {
		req.Header.Del(HeaderVisionRequest)
	}
}

func ApplyBaseURL(req *http.Request, baseURL string) {
	if req == nil || req.URL == nil {
		return
	}
	baseURL = trustedAPIBaseURL(baseURL)
	if baseURL == "" {
		return
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return
	}
	req.URL.Scheme = parsed.Scheme
	req.URL.Host = parsed.Host
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
			"openai_chat":      "openai_responses",
			"openai_responses": "openai_responses",
			"claude":           "openai_responses",
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

type rawQuotaSnapshot struct {
	Entitlement      float64 `json:"entitlement"`
	OverageCount     float64 `json:"overage_count"`
	OveragePermitted bool    `json:"overage_permitted"`
	PercentRemaining float64 `json:"percent_remaining"`
	QuotaID          string  `json:"quota_id"`
	QuotaRemaining   float64 `json:"quota_remaining"`
	Remaining        float64 `json:"remaining"`
	Unlimited        bool    `json:"unlimited"`
}

func normalizeQuotaSnapshot(raw rawQuotaSnapshot) QuotaSnapshot {
	remaining := raw.Remaining
	if remaining == 0 {
		remaining = raw.QuotaRemaining
	}
	entitlement := raw.Entitlement
	reported := raw.Unlimited || raw.PercentRemaining > 0 || entitlement > 0 || remaining > 0
	used := entitlement - remaining
	if used < 0 {
		used = 0
	}
	percentRemaining := raw.PercentRemaining
	if raw.Unlimited {
		percentRemaining = 100
	} else if percentRemaining == 0 && entitlement > 0 {
		percentRemaining = float64(remaining) / float64(entitlement) * 100
	}
	percentRemaining = clampPercent(percentRemaining)
	return QuotaSnapshot{
		Reported:         reported,
		Entitlement:      entitlement,
		Remaining:        remaining,
		Used:             used,
		PercentRemaining: percentRemaining,
		PercentUsed:      clampPercent(100 - percentRemaining),
		Unlimited:        raw.Unlimited,
		OverageCount:     raw.OverageCount,
		OveragePermitted: raw.OveragePermitted,
		QuotaID:          raw.QuotaID,
	}
}

func trustedAPIBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	switch u.Host {
	case "api.githubcopilot.com", "api.individual.githubcopilot.com", "api.business.githubcopilot.com", "copilot-proxy.githubusercontent.com":
		return raw
	default:
		return ""
	}
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func cloneModelInfo(in []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, len(in))
	copy(out, in)
	return out
}

func pruneExpiredAPITokenCache(now time.Time) {
	for key, item := range apiTokenCache.items {
		if !item.expiresAt.After(now) {
			delete(apiTokenCache.items, key)
		}
	}
}

func deleteOneAPITokenCacheEntry() {
	for key := range apiTokenCache.items {
		delete(apiTokenCache.items, key)
		return
	}
}

func pruneExpiredModelCatalogCache(now time.Time) {
	for key, item := range modelCatalogCache.items {
		if !item.expiresAt.After(now) {
			delete(modelCatalogCache.items, key)
		}
	}
}

func deleteOneModelCatalogCacheEntry() {
	for key := range modelCatalogCache.items {
		delete(modelCatalogCache.items, key)
		return
	}
}

func findNumericCapability(value any, targetKey string) (float64, bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.EqualFold(strings.TrimSpace(key), targetKey) {
				if number, ok := numericValue(child); ok {
					return number, true
				}
			}
		}
		for _, child := range v {
			if number, ok := findNumericCapability(child, targetKey); ok {
				return number, true
			}
		}
	case []any:
		for _, child := range v {
			if number, ok := findNumericCapability(child, targetKey); ok {
				return number, true
			}
		}
	}
	return 0, false
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
