package copilot

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNormalizeEnterpriseDomain(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty github.com", in: "", want: ""},
		{name: "domain", in: "company.ghe.com", want: "company.ghe.com"},
		{name: "https URL", in: "https://company.ghe.com/path", want: "company.ghe.com"},
		{name: "invalid", in: "://bad", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEnterpriseDomain(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestBaseURL(t *testing.T) {
	if got := BaseURL(""); got != DefaultBaseURL {
		t.Fatalf("github.com base URL = %q", got)
	}
	if got := BaseURL("github.com"); got != DefaultBaseURL {
		t.Fatalf("explicit github.com base URL = %q", got)
	}
	if got := BaseURL("company.ghe.com"); got != "https://copilot-api.company.ghe.com" {
		t.Fatalf("enterprise base URL = %q", got)
	}
}

func TestCopilotTokenURL(t *testing.T) {
	if got := CopilotTokenURL(""); got != "https://api.github.com/copilot_internal/v2/token" {
		t.Fatalf("github.com token URL = %q", got)
	}
	if got := CopilotTokenURL("company.ghe.com"); got != "https://api.company.ghe.com/copilot_internal/v2/token" {
		t.Fatalf("enterprise token URL = %q", got)
	}
}

func TestExchangeGitHubToken(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.github.com/copilot_internal/v2/token" {
			t.Fatalf("url = %q", req.URL.String())
		}
		if got := req.Header.Get("Authorization"); got != "token github-token" {
			t.Fatalf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"token":"copilot-token"}`))),
		}, nil
	})}

	got, err := ExchangeGitHubToken(context.Background(), client, "", "github-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "copilot-token" {
		t.Fatalf("token = %q", got)
	}
}

func TestPollDeviceFlowExchangesGitHubToken(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case "https://github.com/login/oauth/access_token":
			if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Fatalf("poll Content-Type = %q", got)
			}
			body = `{"access_token":"github-token"}`
		case "https://api.github.com/copilot_internal/v2/token":
			if got := req.Header.Get("Authorization"); got != "token github-token" {
				t.Fatalf("exchange Authorization = %q", got)
			}
			body = `{"token":"copilot-token"}`
		default:
			t.Fatalf("unexpected URL %q", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		}, nil
	})}

	resp, err := PollDeviceFlow(context.Background(), DevicePollRequest{
		ClientID:   "client-id",
		DeviceCode: "device-code",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "success" || resp.AccessToken != "github-token" {
		t.Fatalf("poll response = %#v", resp)
	}
}

func TestStartDeviceFlowUsesDefaultClientID(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("Content-Type = %q", got)
		}
		data, _ := io.ReadAll(req.Body)
		body := string(data)
		if !bytes.Contains(data, []byte("client_id="+DefaultOAuthClientID)) {
			t.Fatalf("body %q does not contain default client id", body)
		}
		if !bytes.Contains(data, []byte("scope=read%3Auser+user%3Aemail")) {
			t.Fatalf("body %q does not contain expected scope", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"verification_uri":"https://github.com/login/device","user_code":"ABCD","device_code":"dev","interval":5}`))),
		}, nil
	})}
	resp, err := StartDeviceFlow(context.Background(), DeviceStartRequest{HTTPClient: client})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.UserCode != "ABCD" || resp.DeviceCode != "dev" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestFetchQuota(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.github.com/copilot_internal/user" {
			t.Fatalf("url = %q", req.URL.String())
		}
		if got := req.Header.Get("Authorization"); got != "token github-token" {
			t.Fatalf("Authorization = %q", got)
		}
		body := `{
			"copilot_plan":"individual",
			"quota_reset_date":"2026-06-01T00:00:00Z",
			"quota_snapshots":{
				"premium_interactions":{"entitlement":300,"remaining":250,"percent_remaining":83.33,"quota_id":"premium"},
				"chat":{"unlimited":true},
				"completions":{"entitlement":1000,"quota_remaining":750}
			}
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		}, nil
	})}
	quota, err := FetchQuota(context.Background(), client, "", "github-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quota.PlanType != "individual" || quota.QuotaResetAt != "2026-06-01T00:00:00Z" {
		t.Fatalf("quota metadata = %#v", quota)
	}
	if quota.Premium.Used != 50 || quota.Premium.Remaining != 250 {
		t.Fatalf("premium snapshot = %#v", quota.Premium)
	}
	if !quota.Chat.Unlimited || quota.Chat.PercentRemaining != 100 {
		t.Fatalf("chat snapshot = %#v", quota.Chat)
	}
	if quota.Completions.PercentRemaining != 75 {
		t.Fatalf("completions snapshot = %#v", quota.Completions)
	}
}

func TestFetchModelCatalogExtractsPremiumCosts(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case "https://api.github.com/copilot_internal/v2/token":
			body = `{"token":"copilot-token","endpoints":{"api":"https://api.individual.githubcopilot.com"}}`
		case "https://api.individual.githubcopilot.com/models":
			if got := req.Header.Get("Authorization"); got != "Bearer copilot-token" {
				t.Fatalf("models Authorization = %q", got)
			}
			body = `{"data":[
				{"id":"gpt-5","model_picker_enabled":true,"capabilities":{"billing":{"premium_interactions":10}}},
				{"id":"gpt-4o","model_picker_enabled":true,"capabilities":{"quota_cost":1}},
				{"id":"disabled","model_picker_enabled":true,"policy":{"state":"disabled"}}
			]}`
		default:
			t.Fatalf("unexpected URL %q", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		}, nil
	})}

	catalog, err := FetchModelCatalog(context.Background(), client, "", "github-token-catalog", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog length = %d, want 2: %#v", len(catalog), catalog)
	}
	if catalog[0].ID != "gpt-4o" || catalog[0].PremiumCost != 1 {
		t.Fatalf("first model = %#v", catalog[0])
	}
	if catalog[1].ID != "gpt-5" || catalog[1].PremiumCost != 10 {
		t.Fatalf("second model = %#v", catalog[1])
	}
}

func TestExtractPremiumCostSupportsNestedAndStringValues(t *testing.T) {
	cost := ExtractPremiumCost(map[string]any{
		"nested": map[string]any{
			"premium_request_cost": "7.5",
		},
	})
	if cost != 7.5 {
		t.Fatalf("cost = %v, want 7.5", cost)
	}
}

func TestPremiumCostForModelIncludesZeroCostModels(t *testing.T) {
	cost, ok := PremiumCostForModel("gpt-5-mini")
	if !ok || cost != 0 {
		t.Fatalf("gpt-5-mini cost = %v/%v, want 0/true", cost, ok)
	}
	cost, ok = PremiumCostForModel("gpt-5.5")
	if !ok || cost != 7.5 {
		t.Fatalf("gpt-5.5 cost = %v/%v, want 7.5/true", cost, ok)
	}
	_, ok = PremiumCostForModel("unknown-model")
	if ok {
		t.Fatal("unknown model should not have a known premium cost")
	}
}

func TestPremiumCostFromOtherSettingsAllowsKnownZeroCost(t *testing.T) {
	cost, ok := PremiumCostFromOtherSettings(`{"copilot_model_prices":{"gpt-5-mini":0}}`, "gpt-5-mini")
	if !ok || cost != 0 {
		t.Fatalf("premium cost = %v/%v, want 0/true", cost, ok)
	}
}

func TestPremiumCostUnitsFromOtherSettingsPreservesFractionalCosts(t *testing.T) {
	cost, ok := PremiumCostUnitsFromOtherSettings(`{"copilot_model_prices":{"gpt-5.4-mini":0.33,"gpt-5.5":7.5}}`, "gpt-5.4-mini")
	if !ok || cost != 33 {
		t.Fatalf("premium cost units = %d/%v, want 33/true", cost, ok)
	}
	cost, ok = PremiumCostUnitsFromOtherSettings(`{"copilot_model_prices":{"gpt-5.4-mini":0.33,"gpt-5.5":7.5}}`, "gpt-5.5")
	if !ok || cost != 750 {
		t.Fatalf("premium cost units = %d/%v, want 750/true", cost, ok)
	}
}

func TestHasVisionInput(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`)
	if !HasVisionInput(body) {
		t.Fatal("expected vision input")
	}
	if HasVisionInput([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)) {
		t.Fatal("did not expect vision input")
	}
}

func TestApplyHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", "old")
	req.Header.Set("authorization", "old")
	ApplyHeaders(req, []byte(`{"input":[{"content":[{"type":"input_image"}]}]}`), "tok")

	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get(HeaderInitiator); got != "user" {
		t.Fatalf("x-initiator = %q", got)
	}
	if got := req.Header.Get(HeaderOpenAIIntent); got != openAIIntentConversationEdits {
		t.Fatalf("Openai-Intent = %q", got)
	}
	if got := req.Header.Get("Editor-Version"); got != "vscode/1.100.0" {
		t.Fatalf("Editor-Version = %q", got)
	}
	if got := req.Header.Get("Editor-Plugin-Version"); got != "copilot/1.300.0" {
		t.Fatalf("Editor-Plugin-Version = %q", got)
	}
	if got := req.Header.Get(HeaderVisionRequest); got != "true" {
		t.Fatalf("Copilot-Vision-Request = %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key should be removed, got %q", got)
	}
}

func TestApplyBaseURLAllowsOnlyTrustedCopilotHosts(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	ApplyBaseURL(req, "https://api.individual.githubcopilot.com")
	if got := req.URL.String(); got != "https://api.individual.githubcopilot.com/chat/completions" {
		t.Fatalf("trusted rewrite = %q", got)
	}

	ApplyBaseURL(req, "https://evil.example.com")
	if got := req.URL.String(); got != "https://api.individual.githubcopilot.com/chat/completions" {
		t.Fatalf("untrusted rewrite changed URL to %q", got)
	}
}

func TestProtocolOverride(t *testing.T) {
	if got := ProtocolOverride("claude-sonnet-4"); got["openai_chat"] != "openai_responses" || got["claude"] != "openai_responses" {
		t.Fatalf("claude override = %v", got)
	}
	if got := ProtocolOverride("gpt-5"); got["openai_chat"] != "openai_responses" {
		t.Fatalf("gpt-5 override = %v", got)
	}
	if got := ProtocolOverride("gpt-5-mini"); got != nil {
		t.Fatalf("gpt-5-mini should not force responses, got %v", got)
	}
	if got := ProtocolOverride("gpt-4.1"); got != nil {
		t.Fatalf("gpt-4.1 should not override, got %v", got)
	}
}
