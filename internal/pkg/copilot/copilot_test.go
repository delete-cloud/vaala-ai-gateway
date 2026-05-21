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

func TestResolveOAuthClientID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "raw client id", in: "Ov23custom", want: "Ov23custom"},
		{name: "opencode alias", in: "opencode", want: openCodeOAuthClientID},
		{name: "copilot api alias", in: "copilot-api", want: openCodeOAuthClientID},
		{name: "trim and case", in: "  OpenCode  ", want: openCodeOAuthClientID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveOAuthClientID(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
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
		if got := req.Header.Get("Authorization"); got != "Bearer github-token" {
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
			body = `{"access_token":"github-token"}`
		case "https://api.github.com/copilot_internal/v2/token":
			if got := req.Header.Get("Authorization"); got != "Bearer github-token" {
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
	if resp.Status != "success" || resp.AccessToken != "copilot-token" {
		t.Fatalf("poll response = %#v", resp)
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
	if got := req.Header.Get(HeaderVisionRequest); got != "true" {
		t.Fatalf("Copilot-Vision-Request = %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key should be removed, got %q", got)
	}
}

func TestProtocolOverride(t *testing.T) {
	if got := ProtocolOverride("claude-sonnet-4"); got["openai_chat"] != "claude" {
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
