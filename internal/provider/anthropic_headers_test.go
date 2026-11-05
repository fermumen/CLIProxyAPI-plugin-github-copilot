package provider

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"cliproxyapi-github-copilot/internal/translate"
	"cliproxyapi-github-copilot/internal/transport"
)

func TestFilterSupportedBetas(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{"none", nil, ""},
		{"keeps supported", []string{"interleaved-thinking-2025-05-14"}, "interleaved-thinking-2025-05-14"},
		{"drops unknown and undated", []string{"claude-code-20250219, context-management, oauth-2025-04-20, context-management-2025-06-27"}, "context-management-2025-06-27"},
		{"joins repeated lines and dedupes", []string{"interleaved-thinking-2025-05-14", " advanced-tool-use-2025-11-20 ,interleaved-thinking-2025-05-14"}, "interleaved-thinking-2025-05-14,advanced-tool-use-2025-11-20"},
		{"rejects family prefix collisions", []string{"interleaved-thinkingX-2025-05-14"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterSupportedBetas(tc.values); got != tc.want {
				t.Fatalf("filterSupportedBetas(%q) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

func TestUpstreamPassthroughHeadersOnlyForNativeClaude(t *testing.T) {
	inbound := http.Header{}
	inbound.Set("Anthropic-Version", "2023-06-01")
	inbound.Set("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14")
	inbound.Set("Authorization", "Bearer client-key")
	inbound.Set("X-Initiator", "agent")

	got := upstreamPassthroughHeaders("claude", translate.EndpointMessages, inbound, true)
	if got.Get("Anthropic-Version") != "2023-06-01" || got.Get("Anthropic-Beta") != "interleaved-thinking-2025-05-14" {
		t.Fatalf("native Claude headers = %#v", got)
	}
	if len(got) != 2 {
		t.Fatalf("forwarded unexpected client headers: %#v", got)
	}

	for _, tc := range []struct{ source, endpoint string }{
		{"claude", translate.EndpointResponses},
		{"claude", translate.EndpointChatCompletions},
		{"openai", translate.EndpointMessages},
		{"openai-response", translate.EndpointMessages},
	} {
		if h := upstreamPassthroughHeaders(tc.source, tc.endpoint, inbound, true); len(h) != 0 {
			t.Fatalf("%s -> %s forwarded %#v", tc.source, tc.endpoint, h)
		}
	}
}

func TestPassthroughHeadersDoNotOverrideCopilotAuth(t *testing.T) {
	inbound := http.Header{}
	inbound.Set("Anthropic-Beta", "context-management-2025-06-27")
	h := withHeaders(inferenceHeaders("copilot-token", true, []byte(`{"messages":[]}`)), anthropicPassthroughHeaders(inbound))
	if h.Get("Authorization") != "Bearer copilot-token" || h.Get("Anthropic-Beta") != "context-management-2025-06-27" {
		t.Fatalf("merged headers = %#v", h)
	}
	if h.Get("Anthropic-Version") != "" {
		t.Fatal("invented an anthropic-version the client did not send")
	}
}

func TestStreamRetryKeepsPassthroughHeaders(t *testing.T) {
	h := &tenantRetryStreamHost{mockHost: mockHost{do: func(context.Context, string, transport.Request) (transport.Response, error) {
		return tokenReply(time.Now(), "synthetic-new-copilot"), nil
	}}}
	s := newTestService(t, h)
	if err := s.Configure([]byte(tenantConfig)); err != nil {
		t.Fatal(err)
	}
	storage := authStorage{GitHubAccessToken: "synthetic-access", GitHubBaseURL: "https://example.ghe.com"}
	token := copilotTokenEntry{Token: "synthetic-expired", APIBaseURL: s.Config().CopilotAPIURL}
	extra := http.Header{}
	extra.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	if _, _, err := s.openModelStream(context.Background(), "", "account", storage, token, translate.EndpointMessages, []byte(`{"messages":[]}`), extra); err != nil {
		t.Fatal(err)
	}
	if len(h.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(h.requests))
	}
	for i, r := range h.requests {
		if r.Headers.Get("Anthropic-Beta") != "interleaved-thinking-2025-05-14" {
			t.Fatalf("request %d lost anthropic-beta: %#v", i, r.Headers)
		}
	}
}

func TestForwardAnthropicBetasSetting(t *testing.T) {
	cfg, err := ParseConfig(nil)
	if err != nil || !cfg.ForwardAnthropicBetas {
		t.Fatalf("default ForwardAnthropicBetas = %v (%v), want true", cfg.ForwardAnthropicBetas, err)
	}
	cfg, err = ParseConfig([]byte("forward_anthropic_betas: false\n"))
	if err != nil || cfg.ForwardAnthropicBetas {
		t.Fatalf("ForwardAnthropicBetas = %v (%v), want false", cfg.ForwardAnthropicBetas, err)
	}
	inbound := http.Header{}
	inbound.Set("Anthropic-Version", "2023-06-01")
	inbound.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	h := upstreamPassthroughHeaders("claude", translate.EndpointMessages, inbound, false)
	if h.Get("Anthropic-Beta") != "" || h.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("disabled betas still forwarded: %#v", h)
	}
}

func betaHeaders() http.Header {
	h := http.Header{}
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("Anthropic-Beta", "context-management-2025-06-27")
	return h
}

func TestRequestRetriesWithoutBetasOnBadRequest(t *testing.T) {
	var requests []transport.Request
	s := newTestService(t, &mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		requests = append(requests, r)
		if r.Headers.Get("Anthropic-Beta") != "" {
			return transport.Response{StatusCode: http.StatusBadRequest, Body: []byte(`{"error":"unsupported beta"}`)}, nil
		}
		return reply(`{"ok":true}`), nil
	}})
	token := copilotTokenEntry{Token: "t", APIBaseURL: s.Config().CopilotAPIURL}
	resp, _, err := s.doModelRequest(context.Background(), "", "account", authStorage{}, token, translate.EndpointMessages, []byte(`{"messages":[]}`), false, betaHeaders())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("fallback failed: %#v %v", resp, err)
	}
	if len(requests) != 2 || requests[1].Headers.Get("Anthropic-Beta") != "" || requests[1].Headers.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("unexpected retry headers: %#v", requests)
	}
}

func TestRequestDoesNotRetryBadRequestWithoutBetas(t *testing.T) {
	calls := 0
	s := newTestService(t, &mockHost{do: func(context.Context, string, transport.Request) (transport.Response, error) {
		calls++
		return transport.Response{StatusCode: http.StatusBadRequest}, nil
	}})
	token := copilotTokenEntry{Token: "t", APIBaseURL: s.Config().CopilotAPIURL}
	if _, _, err := s.doModelRequest(context.Background(), "", "account", authStorage{}, token, translate.EndpointMessages, []byte(`{"messages":[]}`), false, nil); err == nil {
		t.Fatal("expected upstream error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

type badRequestBetaStreamHost struct {
	mockHost
	requests []transport.Request
	closed   []string
}

func (h *badRequestBetaStreamHost) OpenStream(_ context.Context, _ string, r transport.Request) (transport.Stream, error) {
	h.requests = append(h.requests, r)
	status := http.StatusOK
	if r.Headers.Get("Anthropic-Beta") != "" {
		status = http.StatusBadRequest
	}
	return transport.Stream{ID: fmt.Sprint(len(h.requests)), StatusCode: status}, nil
}

func (h *badRequestBetaStreamHost) CloseStream(_ context.Context, id string) error {
	h.closed = append(h.closed, id)
	return nil
}

func TestStreamRetriesWithoutBetasOnBadRequest(t *testing.T) {
	h := &badRequestBetaStreamHost{}
	s := newTestService(t, h)
	token := copilotTokenEntry{Token: "t", APIBaseURL: s.Config().CopilotAPIURL}
	stream, _, err := s.openModelStream(context.Background(), "", "account", authStorage{}, token, translate.EndpointMessages, []byte(`{"messages":[]}`), betaHeaders())
	if err != nil || stream.StatusCode != http.StatusOK || stream.ID != "2" {
		t.Fatalf("stream fallback: %#v %v", stream, err)
	}
	if len(h.closed) != 1 || h.closed[0] != "1" {
		t.Fatalf("rejected stream not closed: %v", h.closed)
	}
	if h.requests[1].Headers.Get("Anthropic-Beta") != "" {
		t.Fatal("retry still sent anthropic-beta")
	}
}
