package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// fold constants — mirror codexcomp/fold.py
const (
	minN       = 1
	encInclude = "reasoning.encrypted_content"
)

var terminalTypes = map[string]bool{
	"response.completed":  true,
	"response.failed":     true,
	"response.incomplete": true,
}

// reasoningTokens extracts reasoning_tokens from usage.output_tokens_details.
func reasoningTokens(usage map[string]any) *int {
	if usage == nil {
		return nil
	}
	details, _ := usage["output_tokens_details"].(map[string]any)
	if details == nil {
		return nil
	}
	raw, ok := details["reasoning_tokens"]
	if !ok || raw == nil {
		return nil
	}
	f, ok := raw.(float64)
	if !ok {
		return nil
	}
	n := int(f)
	return &n
}

// tierN returns n when tokens == 518*n - 2 (516, 1034, 1552, ...), else nil.
func tierN(tokens *int) *int {
	if tokens == nil {
		return nil
	}
	t := *tokens
	if t < defaultTruncationStep-2 || (t+2)%defaultTruncationStep != 0 {
		return nil
	}
	n := (t + 2) / defaultTruncationStep
	return &n
}

// inContinueWindow returns true when n is in [minN, maxTierN].
// A maxTierN of 0 means no upper tier limit.
func inContinueWindow(n *int, maxTierN int) bool {
	if n == nil {
		return false
	}
	v := *n
	if v < minN {
		return false
	}
	if maxTierN != 0 && v > maxTierN {
		return false
	}
	return true
}

// commentaryNudge builds the phase:"commentary" assistant message that provokes
// the model to resume reasoning when replayed with encrypted reasoning items.
func commentaryNudge(markerText string) map[string]any {
	markerText = strings.TrimSpace(markerText)
	if markerText == "" {
		markerText = defaultMarkerText
	}
	return map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{{
			"type": "output_text",
			"text": markerText,
		}},
		"phase": "commentary",
	}
}

// nextRoundBody clones baseBody and reshapes it for a continuation round:
// explicit input, always streamed, encrypted reasoning included, no
// previous_response_id (state is carried in replayed items).
func nextRoundBody(baseBody map[string]any, inputItems []any) map[string]any {
	body := make(map[string]any, len(baseBody))
	for k, v := range baseBody {
		body[k] = v
	}
	body["stream"] = true
	body["input"] = inputItems

	includeRaw, _ := body["include"].([]any)
	include := make([]any, 0, len(includeRaw)+1)
	for _, x := range includeRaw {
		if s, ok := x.(string); ok {
			include = append(include, s)
		}
	}
	hasEnc := false
	for _, s := range include {
		if s == encInclude {
			hasEnc = true
			break
		}
	}
	if !hasEnc {
		include = append(include, encInclude)
	}
	body["include"] = include
	delete(body, "previous_response_id")
	return body
}

// sumUsage accumulates usage into acc (mutates acc in place).
func sumUsage(acc map[string]any, usage map[string]any) {
	if usage == nil {
		return
	}
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if v, ok := usage[key]; ok && v != nil {
			if f, ok := v.(float64); ok {
				acc[key] = toFloat(acc[key]) + f
			}
		}
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); details != nil {
		if cached, ok := details["cached_tokens"].(float64); ok {
			accDetails, _ := acc["input_tokens_details"].(map[string]any)
			if accDetails == nil {
				accDetails = map[string]any{}
				acc["input_tokens_details"] = accDetails
			}
			accDetails["cached_tokens"] = toFloat(accDetails["cached_tokens"]) + cached
		}
	}
	rt := reasoningTokens(usage)
	if rt != nil {
		accDetails, _ := acc["output_tokens_details"].(map[string]any)
		if accDetails == nil {
			accDetails = map[string]any{}
			acc["output_tokens_details"] = accDetails
		}
		accDetails["reasoning_tokens"] = toFloat(accDetails["reasoning_tokens"]) + float64(*rt)
	}
}

// agentUsage builds usage as if the fold were one response: input/cached from
// round 1, reasoning summed, output adds only the final round's non-reasoning part.
func agentUsage(first map[string]any, summed map[string]any, finalRound map[string]any, flushedFinal bool) map[string]any {
	if first == nil {
		first = map[string]any{}
	}
	inTok := toFloat(first["input_tokens"])
	var cached *float64
	if details, _ := first["input_tokens_details"].(map[string]any); details != nil {
		if c, ok := details["cached_tokens"].(float64); ok {
			cached = &c
		}
	}

	reason := float64(0)
	if details, _ := summed["output_tokens_details"].(map[string]any); details != nil {
		if r, ok := details["reasoning_tokens"].(float64); ok {
			reason = r
		}
	}

	finalPart := float64(0)
	if flushedFinal && finalRound != nil {
		out := toFloat(finalRound["output_tokens"])
		rt := reasoningTokens(finalRound)
		rtVal := float64(0)
		if rt != nil {
			rtVal = float64(*rt)
		}
		finalPart = out - rtVal
		if finalPart < 0 {
			finalPart = 0
		}
	}

	usage := map[string]any{
		"input_tokens":  inTok,
		"output_tokens": reason + finalPart,
		"total_tokens":  inTok + reason + finalPart,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": reason,
		},
	}
	if cached != nil {
		usage["input_tokens_details"] = map[string]any{"cached_tokens": *cached}
	}
	return usage
}

// terminalEvent builds the downstream terminal event: round-1 response identity,
// reconstructed output, single-response usage, billed cost and per-round breakdown
// in metadata.
func terminalEvent(
	upstreamTerminal map[string]any,
	baseResponse map[string]any,
	output []map[string]any,
	usage map[string]any,
	rounds []map[string]any,
	billed map[string]any,
	stoppedReason string,
	incompleteReason string,
) map[string]any {
	tresp := map[string]any{}
	if upstreamTerminal != nil {
		if r, ok := upstreamTerminal["response"].(map[string]any); ok {
			tresp = r
		}
	}
	resp := map[string]any{}
	if baseResponse != nil {
		for k, v := range baseResponse {
			resp[k] = v
		}
	} else {
		for k, v := range tresp {
			resp[k] = v
		}
	}
	resp["output"] = output
	resp["usage"] = usage

	metadata := map[string]any{}
	if origMeta, ok := resp["metadata"].(map[string]any); ok {
		for k, v := range origMeta {
			metadata[k] = v
		}
	}
	metadata["proxy_rounds"] = rounds
	metadata["proxy_billed_usage"] = billed
	if stoppedReason != "" {
		metadata["proxy_stopped_reason"] = stoppedReason
	}
	resp["metadata"] = metadata

	if incompleteReason != "" {
		resp["status"] = "incomplete"
		resp["incomplete_details"] = map[string]any{"reason": incompleteReason}
		return map[string]any{
			"type":     "response.incomplete",
			"response": resp,
		}
	}

	status, _ := tresp["status"].(string)
	if status == "" {
		status = "completed"
	}
	resp["status"] = status
	if id, ok := tresp["incomplete_details"]; ok {
		resp["incomplete_details"] = id
	}

	evType, _ := upstreamTerminal["type"].(string)
	if evType == "" {
		evType = "response.completed"
	}
	return map[string]any{
		"type":     evType,
		"response": resp,
	}
}

// failedEvent builds a downstream terminal for a request upstream rejected outright.
func failedEvent(status int, detail string) map[string]any {
	return map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status": "failed",
			"error":  map[string]any{"message": fmt.Sprintf("upstream %d: %s", status, detail), "code": status},
		},
	}
}

func toFloat(v any) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// cloneUsage returns a deep copy of a usage map.
// foldState stores usage references from upstream events; without cloning,
// a reused or mutated upstream event map would silently corrupt firstUsage
// or the current round's usage (Go maps are reference types).
func cloneUsage(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneUsage(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

// hostModelExecRequest is the request sent to host.model.execute_stream.
type hostModelExecRequest struct {
	EntryProtocol  string      `json:"entry_protocol"`
	ExitProtocol   string      `json:"exit_protocol"`
	Model          string      `json:"model"`
	Stream         bool        `json:"stream"`
	Body           []byte      `json:"body"`
	Headers        http.Header `json:"headers,omitempty"`
	Query          url.Values  `json:"query,omitempty"`
	Alt            string      `json:"alt,omitempty"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}
