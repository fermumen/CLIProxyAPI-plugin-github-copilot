package provider

import (
	"net/http"
	"strings"
)

// supportedAnthropicBetas lists beta families (without date suffix) that the
// Copilot Messages endpoint honors. It mirrors SUPPORTED_ANTHROPIC_BETAS in
// VS Code's agent host (src/vs/platform/agentHost/node/claude/anthropicBetas.ts).
// Unknown betas are dropped because the upstream may reject them.
var supportedAnthropicBetas = []string{
	"interleaved-thinking",
	"context-management",
	"advanced-tool-use",
	"per-turn-control",
	"thinking-display-updates",
}

// anthropicPassthroughHeaders returns the client's anthropic-version and the
// supported subset of anthropic-beta. Callers must only apply these when a
// Claude-format request is sent unchanged to the native Messages endpoint.
func anthropicPassthroughHeaders(inbound http.Header) http.Header {
	out := http.Header{}
	if version := strings.TrimSpace(inbound.Get("Anthropic-Version")); version != "" {
		out.Set("Anthropic-Version", version)
	}
	if betas := filterSupportedBetas(inbound.Values("Anthropic-Beta")); betas != "" {
		out.Set("Anthropic-Beta", betas)
	}
	return out
}

// filterSupportedBetas keeps beta identifiers whose family is supported and
// that carry a date suffix (`family-YYYY-MM-DD`). Header lines may repeat or
// contain comma-separated lists; duplicates are removed and order is kept.
func filterSupportedBetas(values []string) string {
	seen := map[string]struct{}{}
	var kept []string
	for _, value := range values {
		for _, beta := range strings.Split(value, ",") {
			beta = strings.TrimSpace(beta)
			if beta == "" || !isSupportedBeta(beta) {
				continue
			}
			if _, dup := seen[beta]; dup {
				continue
			}
			seen[beta] = struct{}{}
			kept = append(kept, beta)
		}
	}
	return strings.Join(kept, ",")
}

func isSupportedBeta(beta string) bool {
	for _, family := range supportedAnthropicBetas {
		if strings.HasPrefix(beta, family+"-") {
			return true
		}
	}
	return false
}

// withoutAnthropicBeta returns a copy of headers without anthropic-beta and
// reports whether there was a beta to remove. Used to retry a request that
// Copilot rejected, so an unsupported beta cannot break a session.
func withoutAnthropicBeta(headers http.Header) (http.Header, bool) {
	if headers.Get("Anthropic-Beta") == "" {
		return nil, false
	}
	out := headers.Clone()
	out.Del("Anthropic-Beta")
	return out, true
}

// withHeaders copies extra into base, replacing existing keys, and returns base.
func withHeaders(base, extra http.Header) http.Header {
	for key, values := range extra {
		base[key] = append([]string(nil), values...)
	}
	return base
}
