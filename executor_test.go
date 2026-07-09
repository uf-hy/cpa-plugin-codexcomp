package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestReasoningTokens(t *testing.T) {
	tests := []struct {
		usage  map[string]any
		expect *int
	}{
		{map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}}, intPtr(516)},
		{map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(1034)}}, intPtr(1034)},
		{map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(0)}}, intPtr(0)},
		{map[string]any{}, nil},
		{nil, nil},
		{map[string]any{"output_tokens_details": map[string]any{}}, nil},
		{map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": "516"}}, nil},
	}
	for _, tt := range tests {
		got := reasoningTokens(tt.usage)
		if tt.expect == nil && got != nil {
			t.Errorf("reasoningTokens(%v) = %d, want nil", tt.usage, *got)
		} else if tt.expect != nil && got == nil {
			t.Errorf("reasoningTokens(%v) = nil, want %d", tt.usage, *tt.expect)
		} else if tt.expect != nil && got != nil && *got != *tt.expect {
			t.Errorf("reasoningTokens(%v) = %d, want %d", tt.usage, *got, *tt.expect)
		}
	}
}

func TestTierN(t *testing.T) {
	tests := []struct {
		tokens int
		expect *int
	}{
		{516, intPtr(1)}, {1034, intPtr(2)}, {1552, intPtr(3)},
		{2070, intPtr(4)}, {2588, intPtr(5)}, {3106, intPtr(6)},
		{515, nil}, {517, nil}, {0, nil}, {1000, nil},
	}
	for _, tt := range tests {
		got := tierN(&tt.tokens)
		if tt.expect == nil && got != nil {
			t.Errorf("tierN(%d) = %d, want nil", tt.tokens, *got)
		} else if tt.expect != nil && got == nil {
			t.Errorf("tierN(%d) = nil, want %d", tt.tokens, *tt.expect)
		} else if tt.expect != nil && got != nil && *got != *tt.expect {
			t.Errorf("tierN(%d) = %d, want %d", tt.tokens, *got, *tt.expect)
		}
	}
}

func TestInContinueWindow(t *testing.T) {
	if !inContinueWindow(intPtr(1), defaultMaxTierN) {
		t.Error("n=1 should be in window")
	}
	if !inContinueWindow(intPtr(6), defaultMaxTierN) {
		t.Error("n=6 should be in window")
	}
	if inContinueWindow(intPtr(7), defaultMaxTierN) {
		t.Error("n=7 should not be in window")
	}
	if inContinueWindow(nil, defaultMaxTierN) {
		t.Error("nil should not be in window")
	}
}

func TestNextRoundBody(t *testing.T) {
	baseBody := map[string]any{
		"model":                "gpt-5.5",
		"stream":               false,
		"input":                []any{map[string]any{"type": "old"}},
		"include":              []any{"text.output"},
		"previous_response_id": "resp_123",
	}
	origInput := []any{map[string]any{"type": "message", "role": "user"}}

	result := nextRoundBody(baseBody, origInput)

	if result["model"] != "gpt-5.5" {
		t.Error("model should be preserved")
	}
	if stream, _ := result["stream"].(bool); !stream {
		t.Error("stream should be true")
	}
	if _, hasPRI := result["previous_response_id"]; hasPRI {
		t.Error("previous_response_id should be deleted")
	}

	inc, _ := result["include"].([]any)
	hasEnc := false
	for _, v := range inc {
		if v == encInclude {
			hasEnc = true
			break
		}
	}
	if !hasEnc {
		t.Error("include should contain reasoning.encrypted_content")
	}

	input, _ := result["input"].([]any)
	if len(input) != 1 {
		t.Errorf("input len = %d, want 1", len(input))
	}
	if !reflect.DeepEqual(input[0], origInput[0]) {
		t.Error("input content should match origInput")
	}

	if _, ok := baseBody["previous_response_id"]; !ok {
		t.Error("original baseBody should not be mutated")
	}
}

func TestNextRoundBodyNilInclude(t *testing.T) {
	baseBody := map[string]any{"model": "gpt-5.5", "input": []any{}}
	result := nextRoundBody(baseBody, []any{})
	inc, _ := result["include"].([]any)
	if len(inc) != 1 || inc[0] != encInclude {
		t.Errorf("include should be [reasoning.encrypted_content], got %v", inc)
	}
}

func TestNextRoundBodyIncludeDedup(t *testing.T) {
	baseBody := map[string]any{
		"include": []any{"reasoning.encrypted_content", "text.output"},
	}
	result := nextRoundBody(baseBody, []any{})
	inc, _ := result["include"].([]any)
	count := 0
	for _, v := range inc {
		if v == encInclude {
			count++
		}
	}
	if count != 1 {
		t.Errorf("should have exactly 1 enc include, got %d", count)
	}
	if len(inc) != 2 {
		t.Errorf("should preserve 2 includes, got %d", len(inc))
	}
}

func TestHasEncryptedContent(t *testing.T) {
	fs := &foldState{}
	if fs.hasEncryptedContent() {
		t.Error("empty should be false")
	}

	fs.roundReasoning = []map[string]any{{"type": "reasoning"}}
	if fs.hasEncryptedContent() {
		t.Error("missing key should be false")
	}

	fs.roundReasoning = []map[string]any{{"encrypted_content": ""}}
	if fs.hasEncryptedContent() {
		t.Error("empty string should be false")
	}

	fs.roundReasoning = []map[string]any{
		{"encrypted_content": "abc"},
		{"type": "reasoning"},
	}
	if fs.hasEncryptedContent() {
		t.Error("only checks last item, last has no enc")
	}

	fs.roundReasoning = []map[string]any{
		{"type": "reasoning"},
		{"encrypted_content": "abc123"},
	}
	if !fs.hasEncryptedContent() {
		t.Error("last item with non-empty enc should be true")
	}
}

func TestShouldContinue(t *testing.T) {
	fs := &foldState{
		terminal:       map[string]any{"type": "response.completed"},
		usage:          map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		roundReasoning: []map[string]any{{"encrypted_content": "abc"}},
		config:         defaultFoldConfig(),
	}
	fs.roundNo = 1
	if !fs.shouldContinue() {
		t.Error("516 + enc + round 1 should continue")
	}

	fs.roundNo = defaultMaxContinue + 1
	if fs.shouldContinue() {
		t.Error("should not continue beyond maxContinue")
	}
}

func TestShouldContinueNoEnc(t *testing.T) {
	fs := &foldState{
		terminal:       map[string]any{"type": "response.completed"},
		usage:          map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		roundReasoning: []map[string]any{{"type": "reasoning"}},
		config:         defaultFoldConfig(),
	}
	fs.roundNo = 1
	if fs.shouldContinue() {
		t.Error("no enc should not continue")
	}
}

func TestShouldContinueNoTruncation(t *testing.T) {
	fs := &foldState{
		terminal:       map[string]any{"type": "response.completed"},
		usage:          map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(100)}},
		roundReasoning: []map[string]any{{"encrypted_content": "abc"}},
		config:         defaultFoldConfig(),
	}
	fs.roundNo = 1
	if fs.shouldContinue() {
		t.Error("non-truncation should not continue")
	}
}

func TestShouldContinueNilTerminal(t *testing.T) {
	fs := &foldState{usage: map[string]any{}, config: defaultFoldConfig()}
	fs.roundNo = 1
	if fs.shouldContinue() {
		t.Error("nil terminal should not continue")
	}
}

func TestStoppedReason(t *testing.T) {
	fs := &foldState{
		terminal:       map[string]any{"type": "response.completed"},
		usage:          map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		roundReasoning: []map[string]any{{"type": "reasoning"}},
		config:         defaultFoldConfig(),
	}
	if fs.stoppedReason() != "no_encrypted_content" {
		t.Errorf("got %s, want no_encrypted_content", fs.stoppedReason())
	}

	fs.roundReasoning = []map[string]any{{"encrypted_content": "abc"}}
	fs.roundNo = defaultMaxContinue + 1
	if fs.stoppedReason() != "max_continue" {
		t.Errorf("got %s, want max_continue", fs.stoppedReason())
	}

	fs.roundNo = 1
	fs.usage = map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(3624)}}
	if fs.stoppedReason() != "tier_out_of_window" {
		t.Errorf("got %s, want tier_out_of_window (n=7)", fs.stoppedReason())
	}

	fs.usage = map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(100)}}
	if fs.stoppedReason() != "" {
		t.Errorf("non-truncation should be empty, got %s", fs.stoppedReason())
	}
}

func TestSumUsage(t *testing.T) {
	acc := map[string]any{}
	u1 := map[string]any{
		"input_tokens": float64(100), "output_tokens": float64(516), "total_tokens": float64(616),
		"input_tokens_details":  map[string]any{"cached_tokens": float64(50)},
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)},
	}
	u2 := map[string]any{
		"input_tokens": float64(200), "output_tokens": float64(100), "total_tokens": float64(300),
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(50)},
	}
	sumUsage(acc, u1)
	sumUsage(acc, u2)

	if acc["input_tokens"].(float64) != 300 {
		t.Errorf("input: got %v, want 300", acc["input_tokens"])
	}
	if acc["output_tokens"].(float64) != 616 {
		t.Errorf("output: got %v, want 616", acc["output_tokens"])
	}
	if acc["total_tokens"].(float64) != 916 {
		t.Errorf("total: got %v, want 916", acc["total_tokens"])
	}

	details, _ := acc["input_tokens_details"].(map[string]any)
	if details["cached_tokens"].(float64) != 50 {
		t.Errorf("cached: got %v", details["cached_tokens"])
	}

	odetails, _ := acc["output_tokens_details"].(map[string]any)
	if odetails["reasoning_tokens"].(float64) != 566 {
		t.Errorf("reasoning: got %v", odetails["reasoning_tokens"])
	}
}

func TestSumUsageNil(t *testing.T) {
	acc := map[string]any{"input_tokens": float64(100)}
	sumUsage(acc, nil)
	if acc["input_tokens"].(float64) != 100 {
		t.Error("nil usage should not change acc")
	}
}

func TestAgentUsage(t *testing.T) {
	first := map[string]any{
		"input_tokens":         float64(1000),
		"input_tokens_details": map[string]any{"cached_tokens": float64(500)},
	}
	summed := map[string]any{
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(566)},
	}
	finalRound := map[string]any{
		"output_tokens":         float64(100),
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(50)},
	}

	usage := agentUsage(first, summed, finalRound, true)

	if usage["input_tokens"].(float64) != 1000 {
		t.Errorf("input: got %v", usage["input_tokens"])
	}
	if usage["output_tokens"].(float64) != 616 {
		t.Errorf("output should be 566+50=616, got %v", usage["output_tokens"])
	}
	if usage["total_tokens"].(float64) != 1616 {
		t.Errorf("total: got %v", usage["total_tokens"])
	}

	od, _ := usage["output_tokens_details"].(map[string]any)
	if od["reasoning_tokens"].(float64) != 566 {
		t.Errorf("reasoning: got %v", od["reasoning_tokens"])
	}

	id, _ := usage["input_tokens_details"].(map[string]any)
	if id["cached_tokens"].(float64) != 500 {
		t.Errorf("cached: got %v", id["cached_tokens"])
	}
}

func TestAgentUsageFinalPartNegative(t *testing.T) {
	first := map[string]any{"input_tokens": float64(100)}
	summed := map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(100)}}
	finalRound := map[string]any{
		"output_tokens":         float64(50),
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(100)},
	}
	usage := agentUsage(first, summed, finalRound, true)
	if usage["output_tokens"].(float64) != 100 {
		t.Errorf("output should be 100+0=100 (finalPart clamped), got %v", usage["output_tokens"])
	}
}

func TestCloneUsageIsolation(t *testing.T) {
	original := map[string]any{
		"input_tokens":         float64(1000),
		"input_tokens_details": map[string]any{"cached_tokens": float64(500)},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": float64(516),
		},
	}
	clone := cloneUsage(original)

	original["input_tokens"] = float64(9999)
	od, _ := original["input_tokens_details"].(map[string]any)
	od["cached_tokens"] = float64(9999)
	ood, _ := original["output_tokens_details"].(map[string]any)
	ood["reasoning_tokens"] = float64(9999)

	if clone["input_tokens"].(float64) != 1000 {
		t.Errorf("clone input corrupted: got %v, want 1000", clone["input_tokens"])
	}
	cd, _ := clone["input_tokens_details"].(map[string]any)
	if cd["cached_tokens"].(float64) != 500 {
		t.Errorf("clone cached corrupted: got %v, want 500", cd["cached_tokens"])
	}
	cod, _ := clone["output_tokens_details"].(map[string]any)
	if cod["reasoning_tokens"].(float64) != 516 {
		t.Errorf("clone reasoning corrupted: got %v, want 516", cod["reasoning_tokens"])
	}
}

func TestCloneUsageNil(t *testing.T) {
	if cloneUsage(nil) != nil {
		t.Error("cloneUsage(nil) should return nil")
	}
}

func TestAgentUsageNoFinalFlush(t *testing.T) {
	first := map[string]any{"input_tokens": float64(100)}
	summed := map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(500)}}
	finalRound := map[string]any{"output_tokens": float64(200)}
	usage := agentUsage(first, summed, finalRound, false)
	if usage["output_tokens"].(float64) != 500 {
		t.Errorf("output should be 500 (no final), got %v", usage["output_tokens"])
	}
}

func TestTerminalEvent(t *testing.T) {
	upstreamTerminal := map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "resp_123", "status": "completed"},
	}
	baseResponse := map[string]any{
		"id": "resp_456", "model": "gpt-5.5", "created_at": float64(1234567890),
	}
	output := []map[string]any{{"type": "message", "id": "msg_1"}}
	usage := map[string]any{"input_tokens": float64(100), "output_tokens": float64(200)}
	rounds := []map[string]any{{"round": float64(1), "n": intPtr(1)}}
	billed := map[string]any{"input_tokens": float64(100)}
	stoppedReason := "max_continue"

	ev := terminalEvent(upstreamTerminal, baseResponse, output, usage, rounds, billed, stoppedReason, "")

	if ev["type"] != "response.completed" {
		t.Errorf("type: got %v", ev["type"])
	}
	resp, _ := ev["response"].(map[string]any)
	if resp["id"] != "resp_456" {
		t.Errorf("should use baseResponse id, got %v", resp["id"])
	}
	if resp["model"] != "gpt-5.5" {
		t.Error("model should be preserved")
	}
	if resp["status"] != "completed" {
		t.Errorf("status: got %v", resp["status"])
	}

	meta, _ := resp["metadata"].(map[string]any)
	if meta["proxy_rounds"] == nil {
		t.Error("should have proxy_rounds")
	}
	if meta["proxy_billed_usage"] == nil {
		t.Error("should have proxy_billed_usage")
	}
	if meta["proxy_stopped_reason"] != "max_continue" {
		t.Error("should have stopped_reason")
	}

	out, _ := resp["output"].([]map[string]any)
	if len(out) != 1 {
		t.Errorf("output len: got %d", len(out))
	}
}

func TestTerminalEventIncomplete(t *testing.T) {
	ev := terminalEvent(nil, map[string]any{"id": "resp_1"}, nil, nil, nil, nil, "", "upstream_eof")
	if ev["type"] != "response.incomplete" {
		t.Errorf("type: got %v", ev["type"])
	}
	resp, _ := ev["response"].(map[string]any)
	if resp["status"] != "incomplete" {
		t.Errorf("status: got %v", resp["status"])
	}
	id, _ := resp["incomplete_details"].(map[string]any)
	if id["reason"] != "upstream_eof" {
		t.Errorf("reason: got %v", id["reason"])
	}
}

func TestFailedEvent(t *testing.T) {
	ev := failedEvent(429, "rate limited")
	if ev["type"] != "response.failed" {
		t.Errorf("type: got %v", ev["type"])
	}
	resp, _ := ev["response"].(map[string]any)
	if resp["status"] != "failed" {
		t.Errorf("status: got %v", resp["status"])
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj["code"].(int) != 429 {
		t.Errorf("code: got %v", errObj["code"])
	}
}

func TestCommentaryNudge(t *testing.T) {
	nudge := commentaryNudge("Keep reasoning")
	if nudge["type"] != "message" {
		t.Error("type should be message")
	}
	if nudge["role"] != "assistant" {
		t.Error("role should be assistant")
	}
	if nudge["phase"] != "commentary" {
		t.Error("phase should be commentary")
	}
	content, _ := nudge["content"].([]map[string]any)
	if len(content) != 1 {
		t.Error("should have 1 content part")
	}
	if content[0]["text"] != "Keep reasoning" {
		t.Error("text should use configured marker text")
	}
}

func TestCommentaryNudgeDefault(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\t"} {
		nudge := commentaryNudge(input)
		content, _ := nudge["content"].([]map[string]any)
		if content[0]["text"] != defaultMarkerText {
			t.Errorf("input %q should fall back to default marker text", input)
		}
	}
}

func TestDecodeFoldConfigMarkerText(t *testing.T) {
	cfg, err := decodeFoldConfig([]byte("enabled: true\nmarker_text: 'Spend time on thinking; you do not need to use the commentary channel to report progress to me.'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MarkerText != "Spend time on thinking; you do not need to use the commentary channel to report progress to me." {
		t.Errorf("unexpected marker_text: %q", cfg.MarkerText)
	}
}

func TestApplyLifecycleConfig(t *testing.T) {
	previous := currentFoldConfig()
	defer setFoldConfig(previous)

	payload, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("marker_text: Custom marker\nmax_continue: 5\nmax_tier_n: 8\ndebug_log: true")})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyLifecycleConfig(payload); err != nil {
		t.Fatal(err)
	}
	cfg := currentFoldConfig()
	if cfg.MarkerText != "Custom marker" {
		t.Errorf("MarkerText = %q", cfg.MarkerText)
	}
	if cfg.MaxContinue != 5 {
		t.Errorf("MaxContinue = %d", cfg.MaxContinue)
	}
	if cfg.MaxTierN != 8 {
		t.Errorf("MaxTierN = %d", cfg.MaxTierN)
	}
	if !cfg.DebugLog {
		t.Error("DebugLog should be true")
	}
}

func TestApplyLifecycleConfigEmptyRawInstallsDefaults(t *testing.T) {
	previous := currentFoldConfig()
	defer setFoldConfig(previous)

	setFoldConfig(foldConfig{MarkerText: "stale", MaxContinue: 99, MaxTierN: 99, DebugLog: true})
	if err := applyLifecycleConfig(nil); err != nil {
		t.Fatalf("empty raw should not error: %v", err)
	}
	cfg := currentFoldConfig()
	if cfg != defaultFoldConfig() {
		t.Errorf("empty raw should install defaults, got %+v", cfg)
	}
}

func TestDecodeFoldConfigFromJSON(t *testing.T) {
	cfg, err := decodeFoldConfig([]byte(`{"marker_text":"JSON marker","max_continue":2,"max_tier_n":0,"debug_log":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MarkerText != "JSON marker" || cfg.MaxContinue != 2 || cfg.MaxTierN != 0 || !cfg.DebugLog {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestDecodeFoldConfigRejectsInvalidValues(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("max_continue: -1"),
		[]byte("max_tier_n: nope"),
		[]byte("debug_log: maybe"),
	} {
		if _, err := decodeFoldConfig(raw); err == nil {
			t.Fatalf("expected error for %q", string(raw))
		}
	}
}

func TestShouldContinueConfigurableMaxContinue(t *testing.T) {
	fs := &foldState{
		terminal:       map[string]any{"type": "response.completed"},
		usage:          map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		roundReasoning: []map[string]any{{"encrypted_content": "abc"}},
		config:         foldConfig{MarkerText: defaultMarkerText, MaxTierN: defaultMaxTierN, MaxContinue: 0},
	}
	fs.roundNo = 1
	if fs.shouldContinue() {
		t.Error("max_continue=0 should disable continuation")
	}
}

func TestPrepareNextRound(t *testing.T) {
	fs := &foldState{
		roundReasoning: []map[string]any{
			{"type": "reasoning", "encrypted_content": "abc"},
			{"type": "reasoning", "encrypted_content": "def"},
		},
	}
	fs.prepareNextRound()

	if len(fs.replayTail) != 3 {
		t.Errorf("replayTail should have 2 reasoning + 1 nudge = 3, got %d", len(fs.replayTail))
	}

	last := fs.replayTail[2].(map[string]any)
	if last["phase"] != "commentary" {
		t.Error("last item should be commentary nudge")
	}
}

func TestProcessEventResponseCreated(t *testing.T) {
	fs := &foldState{seq: 0}
	fs.roundNo = 1

	ev := map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": "resp_123"},
	}
	term, err := fs.processEvent(ev, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if term != nil {
		t.Error("response.created should not be terminal")
	}
	if fs.baseResponse == nil || fs.baseResponse["id"] != "resp_123" {
		t.Error("baseResponse should be set")
	}
	if ev["sequence_number"].(int) != 0 {
		t.Error("should stamp seq=0")
	}
}

func TestProcessEventResponseCreatedRound2(t *testing.T) {
	fs := &foldState{}
	fs.roundNo = 2
	ev := map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_456"}}
	term, _ := fs.processEvent(ev, "")
	if term != nil {
		t.Error("should not be terminal")
	}
	if fs.baseResponse != nil {
		t.Error("baseResponse should not be set in round 2+")
	}
}

func TestProcessEventTerminal(t *testing.T) {
	fs := &foldState{}
	terminal := map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"usage": map[string]any{"output_tokens": float64(100)}},
	}
	term, _ := fs.processEvent(terminal, "")
	if term == nil {
		t.Error("should return terminal")
	}
	if fs.terminal == nil {
		t.Error("fs.terminal should be set")
	}
	if fs.usage == nil {
		t.Error("fs.usage should be set")
	}
}

func TestProcessEventReasoningAdded(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	ev := map[string]any{
		"type":         "response.output_item.added",
		"output_index": float64(0),
		"item":         map[string]any{"type": "reasoning", "id": "rs_1"},
	}
	term, err := fs.processEvent(ev, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if term != nil {
		t.Error("should not be terminal")
	}
	if fs.kind[0] != "reasoning" {
		t.Error("kind[0] should be reasoning")
	}
	if fs.oiToDS[0] != 0 {
		t.Error("oiToDS[0] should be 0")
	}
	if ev["output_index"].(int) != 0 {
		t.Error("output_index should be dsOI=0")
	}
	if fs.dsOI != 1 {
		t.Error("dsOI should increment to 1")
	}
}

func TestProcessEventBufferedAdded(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	ev := map[string]any{
		"type":         "response.output_item.added",
		"output_index": float64(1),
		"item":         map[string]any{"type": "message", "id": "msg_1"},
	}
	term, _ := fs.processEvent(ev, "")
	if term != nil {
		t.Error("should not be terminal")
	}
	if fs.kind[1] != "buffered" {
		t.Error("kind[1] should be buffered")
	}
	if len(fs.buffered) != 1 {
		t.Error("should have 1 buffered entry")
	}
}

func TestProcessEventReasoningDone(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1
	fs.kind[0] = "reasoning"
	fs.oiToDS[0] = 0
	fs.dsOI = 1

	ev := map[string]any{
		"type":         "response.output_item.done",
		"output_index": float64(0),
		"item":         map[string]any{"type": "reasoning", "encrypted_content": "abc123"},
	}
	fs.processEvent(ev, "")

	if len(fs.roundReasoning) != 1 {
		t.Error("should append to roundReasoning")
	}
	if len(fs.finalOutput) != 1 {
		t.Error("should append to finalOutput")
	}
	if fs.roundReasoning[0]["encrypted_content"] != "abc123" {
		t.Error("should preserve encrypted_content")
	}
}

func TestProcessEventBufferedDone(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1
	fs.kind[1] = "buffered"
	fs.buffered = []bufferedEntry{{oi: 1, item: nil, events: []map[string]any{}}}

	ev := map[string]any{
		"type":         "response.output_item.done",
		"output_index": float64(1),
		"item":         map[string]any{"type": "message", "id": "msg_1"},
	}
	fs.processEvent(ev, "")

	if fs.buffered[0].item == nil {
		t.Error("should set buffered item")
	}
	if len(fs.buffered[0].events) != 1 {
		t.Errorf("should have 1 events, got %d", len(fs.buffered[0].events))
	}
}

func TestProcessEventUnknownKindForwarded(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	ev := map[string]any{
		"type":         "response.reasoning_text.delta",
		"output_index": float64(99),
	}
	term, _ := fs.processEvent(ev, "")
	if term != nil {
		t.Error("should not be terminal")
	}
	if ev["sequence_number"] == nil {
		t.Error("should be stamped for unknown kind")
	}
}

func TestProcessEventFailedNotContinued(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	}, "")
	fs.processEvent(map[string]any{
		"type": "response.output_item.done", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "abc"},
	}, "")

	terminal := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"usage": map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		},
	}
	fs.processEvent(terminal, "")

	if fs.shouldContinue() {
		t.Error("response.failed should not trigger continuation even with 516 tokens and enc content")
	}
}

func TestProcessEventIncompleteNotContinued(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	}, "")
	fs.processEvent(map[string]any{
		"type": "response.output_item.done", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "abc"},
	}, "")

	terminal := map[string]any{
		"type": "response.incomplete",
		"response": map[string]any{
			"usage": map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		},
	}
	fs.processEvent(terminal, "")

	if fs.shouldContinue() {
		t.Error("response.incomplete should not trigger continuation even with 516 tokens and enc content")
	}
}

func TestStoppedReasonEmptyForFailed(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	}, "")
	fs.processEvent(map[string]any{
		"type": "response.output_item.done", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "abc"},
	}, "")

	fs.processEvent(map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"usage": map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		},
	}, "")

	if fs.stoppedReason() != "" {
		t.Errorf("stoppedReason should be empty for response.failed, got %s", fs.stoppedReason())
	}

	ev := fs.terminalEvent()
	resp, _ := ev["response"].(map[string]any)
	meta, _ := resp["metadata"].(map[string]any)
	if sr, has := meta["proxy_stopped_reason"]; has {
		t.Errorf("should not have proxy_stopped_reason for failed, got %v", sr)
	}
}

func TestStoppedReasonEmptyForIncomplete(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1

	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	}, "")
	fs.processEvent(map[string]any{
		"type": "response.output_item.done", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "abc"},
	}, "")

	fs.processEvent(map[string]any{
		"type": "response.incomplete",
		"response": map[string]any{
			"usage": map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)}},
		},
	}, "")

	if fs.stoppedReason() != "" {
		t.Errorf("stoppedReason should be empty for response.incomplete, got %s", fs.stoppedReason())
	}

	ev := fs.terminalEvent()
	resp, _ := ev["response"].(map[string]any)
	meta, _ := resp["metadata"].(map[string]any)
	if sr, has := meta["proxy_stopped_reason"]; has {
		t.Errorf("should not have proxy_stopped_reason for incomplete, got %v", sr)
	}
}

func TestFlushCleanStop(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1
	fs.dsOI = 5

	fs.buffered = []bufferedEntry{
		{oi: 1, item: map[string]any{"type": "message", "id": "msg_1"},
			events: []map[string]any{
				{"type": "response.output_item.added", "output_index": float64(1), "item": map[string]any{"type": "message"}},
				{"type": "response.output_item.done", "output_index": float64(1), "item": map[string]any{"type": "message", "id": "msg_1"}},
			}},
		{oi: 2, item: map[string]any{"type": "tool_call", "id": "tc_1"},
			events: []map[string]any{
				{"type": "response.output_item.added", "output_index": float64(2), "item": map[string]any{"type": "tool_call"}},
			}},
	}

	err := fs.flushCleanStop("")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	for _, ev := range fs.buffered[0].events {
		if ev["output_index"].(int) != 5 {
			t.Errorf("all events in first entry should have output_index=5, got %v", ev["output_index"])
		}
	}
	for _, ev := range fs.buffered[1].events {
		if ev["output_index"].(int) != 6 {
			t.Errorf("all events in second entry should have output_index=6, got %v", ev["output_index"])
		}
	}
	if fs.dsOI != 7 {
		t.Errorf("dsOI should be 7, got %d", fs.dsOI)
	}
	if len(fs.finalOutput) != 2 {
		t.Error("should have 2 finalOutput items")
	}
}

func TestFlushCleanStopSequenceNumbers(t *testing.T) {
	fs := newFoldState(map[string]any{}, []any{}, rpcExecutorRequest{}, "")
	fs.roundNo = 1
	fs.seq = 10
	fs.dsOI = 0

	fs.buffered = []bufferedEntry{
		{oi: 0, item: map[string]any{"type": "message"},
			events: []map[string]any{{"type": "response.output_item.added", "output_index": float64(0)}}},
		{oi: 1, item: map[string]any{"type": "message"},
			events: []map[string]any{{"type": "response.output_item.added", "output_index": float64(1)}}},
	}

	fs.flushCleanStop("")

	if fs.buffered[0].events[0]["sequence_number"].(int) != 10 {
		t.Errorf("first seq should be 10, got %v", fs.buffered[0].events[0]["sequence_number"])
	}
	if fs.buffered[1].events[0]["sequence_number"].(int) != 11 {
		t.Errorf("second seq should be 11, got %v", fs.buffered[1].events[0]["sequence_number"])
	}
}

func TestTwoRoundHappyPath(t *testing.T) {
	fs := newFoldState(
		map[string]any{"model": "gpt-5.5"},
		[]any{map[string]any{"type": "message", "role": "user"}},
		rpcExecutorRequest{}, "",
	)
	fs.roundNo = 1

	// Round 1: response.created
	fs.processEvent(map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": "resp_1", "model": "gpt-5.5", "created_at": float64(1000)},
	}, "")
	if fs.baseResponse["id"] != "resp_1" {
		t.Error("baseResponse should be set")
	}

	// Round 1: reasoning item added + done
	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	}, "")
	fs.processEvent(map[string]any{
		"type": "response.output_item.done", "output_index": float64(0),
		"item": map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "enc_abc"},
	}, "")
	if len(fs.roundReasoning) != 1 {
		t.Error("should have 1 roundReasoning")
	}
	if !fs.hasEncryptedContent() {
		t.Error("should have encrypted content")
	}

	// Round 1: buffered message (should NOT leak to finalOutput yet)
	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(1),
		"item": map[string]any{"type": "message", "id": "msg_bad"},
	}, "")
	if len(fs.finalOutput) != 1 {
		t.Error("buffered item should not enter finalOutput yet")
	}

	// Round 1: terminal (truncated at 516)
	round1Usage := map[string]any{
		"output_tokens":         float64(516),
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(516)},
	}
	terminal1 := map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "resp_1", "usage": round1Usage},
	}
	term, _ := fs.processEvent(terminal1, "")
	if term == nil {
		t.Fatal("should return terminal")
	}

	fs.endRound(terminal1, round1Usage)
	if !fs.shouldContinue() {
		t.Error("should continue after 516 truncation")
	}

	fs.prepareNextRound()
	if len(fs.replayTail) != 2 {
		t.Errorf("replayTail should have 1 reasoning + 1 nudge = 2, got %d", len(fs.replayTail))
	}

	// Round 2: clean stop
	fs.roundNo = 2
	fs.roundReasoning = nil
	fs.kind = map[int]string{}
	fs.oiToDS = map[int]int{}
	fs.buffered = nil
	fs.terminal = nil
	fs.usage = nil

	// Round 2: message (buffered, the real answer)
	fs.processEvent(map[string]any{
		"type": "response.output_item.added", "output_index": float64(0),
		"item": map[string]any{"type": "message", "id": "msg_good"},
	}, "")
	fs.processEvent(map[string]any{
		"type": "response.output_item.done", "output_index": float64(0),
		"item": map[string]any{"type": "message", "id": "msg_good", "content": []any{map[string]any{"text": "21"}}},
	}, "")

	round2Usage := map[string]any{
		"output_tokens":         float64(100),
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(50)},
	}
	terminal2 := map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "resp_1", "usage": round2Usage},
	}
	fs.processEvent(terminal2, "")
	fs.endRound(terminal2, round2Usage)

	if fs.shouldContinue() {
		t.Error("round 2 should not continue (50 tokens, not truncation)")
	}

	// Flush buffered
	fs.flushCleanStop("")

	// Final terminal
	finalEv := fs.terminalEvent()
	if finalEv["type"] != "response.completed" {
		t.Error("should be completed")
	}
	resp, _ := finalEv["response"].(map[string]any)
	if resp["id"] != "resp_1" {
		t.Error("should use baseResponse id")
	}

	output, _ := resp["output"].([]map[string]any)
	if len(output) != 2 {
		t.Errorf("finalOutput should have 1 reasoning + 1 message = 2, got %d", len(output))
	}
	if output[1]["id"] != "msg_good" {
		t.Error("second output should be msg_good, not msg_bad")
	}

	meta, _ := resp["metadata"].(map[string]any)
	rounds, _ := meta["proxy_rounds"].([]map[string]any)
	if len(rounds) != 2 {
		t.Errorf("should have 2 rounds, got %d", len(rounds))
	}

	usage, _ := resp["usage"].(map[string]any)
	od, _ := usage["output_tokens_details"].(map[string]any)
	if od["reasoning_tokens"].(float64) != 566 {
		t.Errorf("reasoning should be 516+50=566, got %v", od["reasoning_tokens"])
	}
}

func TestFindJSONEnd(t *testing.T) {
	tests := []struct {
		input  string
		expect int
	}{
		{`{"a":1}`, 6},
		{`{"a":{"b":2}}`, 12},
		{`{"a":"}"}`, 8},
		{`{}`, 1},
		{`{"a":1`, -1},
		{``, -1},
	}
	for _, tt := range tests {
		got := findJSONEnd([]byte(tt.input), 0)
		if got != tt.expect {
			t.Errorf("findJSONEnd(%q) = %d, want %d", tt.input, got, tt.expect)
		}
	}
}

func TestFindJSONEndEscapedBackslash(t *testing.T) {
	input := `{"a":"b\\"}`
	got := findJSONEnd([]byte(input), 0)
	if got != len(input)-1 {
		t.Errorf("got %d, want %d", got, len(input)-1)
	}
}

func TestFindJSONEndEscapedBrace(t *testing.T) {
	input := `{"a":"{\"}"}`
	got := findJSONEnd([]byte(input), 0)
	if got != len(input)-1 {
		t.Errorf("got %d, want %d", got, len(input)-1)
	}
}

func TestFindSubstring(t *testing.T) {
	tests := []struct {
		data, sub string
		expect    int
	}{
		{"data: hello", "data:", 0},
		{"event: x\ndata: hello", "data:", 9},
		{"no match", "data:", -1},
		{"", "data:", -1},
		{"da", "data:", -1},
	}
	for _, tt := range tests {
		got := findSubstring([]byte(tt.data), []byte(tt.sub))
		if got != tt.expect {
			t.Errorf("findSubstring(%q,%q)=%d, want %d", tt.data, tt.sub, got, tt.expect)
		}
	}
}

func TestSSEBufferAccumulation(t *testing.T) {
	fs := &foldState{}
	term, err := fs.processAndEmit([]byte("event: response.created"), "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if term != nil {
		t.Fatal("should not have terminal")
	}
}

func TestSSEBufferChunkedJSON(t *testing.T) {
	fs := &foldState{}
	fullJSON := `{"type":"response.completed","response":{"id":"r","usage":{"output_tokens_details":{"reasoning_tokens":0}}}}`
	part1 := `data: ` + fullJSON[:20]
	part2 := fullJSON[20:]
	_, err := fs.processAndEmit([]byte(part1), "")
	if err != nil {
		t.Fatalf("part1 error: %v", err)
	}
	term, err := fs.processAndEmit([]byte(part2), "")
	if err != nil {
		t.Fatalf("part2 error: %v", err)
	}
	if term == nil {
		t.Fatal("should have terminal")
	}
}

func TestSSEMultipleEventsOnePayload(t *testing.T) {
	fs := &foldState{}
	fs.roundNo = 1
	payload := `data: {"type":"response.created","response":{"id":"r1"}}` +
		`data: {"type":"response.in_progress","response":{"id":"r1"}}`
	_, err := fs.processAndEmit([]byte(payload), "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if fs.baseResponse == nil || fs.baseResponse["id"] != "r1" {
		t.Error("baseResponse should be set from response.created")
	}
	if fs.seq != 2 {
		t.Errorf("should have stamped 2 events, got seq=%d", fs.seq)
	}
}

func TestSSEDoneHandled(t *testing.T) {
	fs := &foldState{}
	_, err := fs.processAndEmit([]byte("data: [DONE]"), "")
	if err != nil {
		t.Fatalf("[DONE] error: %v", err)
	}
}

func TestSSEBufferLimit(t *testing.T) {
	fs := &foldState{}
	fs.sseBuffer = make([]byte, maxSSEBufferSize-1)
	_, err := fs.processAndEmit([]byte("data: x"), "")
	if err == nil {
		t.Fatal("should error")
	}
}

func TestSSEDataColonNoSpace(t *testing.T) {
	fs := &foldState{}
	fs.roundNo = 1
	payload := `data:{"type":"response.created","response":{"id":"r"}}`
	_, err := fs.processAndEmit([]byte(payload), "")
	if err != nil {
		t.Fatalf("data: without space should work: %v", err)
	}
	if fs.baseResponse == nil || fs.baseResponse["id"] != "r" {
		t.Error("should parse event and set baseResponse")
	}
}

func TestSSEChunkSplitAtDataPrefix(t *testing.T) {
	fs := &foldState{}
	fs.roundNo = 1
	_, err := fs.processAndEmit([]byte("da"), "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	_, err = fs.processAndEmit([]byte("ta: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}"), "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if fs.baseResponse == nil || fs.baseResponse["id"] != "r" {
		t.Error("should parse event after split chunks")
	}
}

func TestStamp(t *testing.T) {
	fs := &foldState{seq: 5}
	ev := map[string]any{"type": "response.created"}
	fs.stamp(ev)
	if ev["sequence_number"].(int) != 5 {
		t.Error("seq should be 5")
	}
	if fs.seq != 6 {
		t.Error("seq should increment to 6")
	}
}

func TestUpstreamError(t *testing.T) {
	ue := &upstreamError{status: 429, msg: "rate limited"}
	if ue.Error() != "rate limited" {
		t.Errorf("got %s", ue.Error())
	}
	if ue.status != 429 {
		t.Errorf("got %d", ue.status)
	}
}

func TestMidStreamError(t *testing.T) {
	me := &midStreamError{msg: "connection reset"}
	if me.Error() != "connection reset" {
		t.Errorf("got %s", me.Error())
	}
}

func TestRouteModelDeclines(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"string input", `{"model":"gpt-5.5","stream":true,"input":"hello"}`},
		{"previous_response_id", `{"model":"gpt-5.5","stream":true,"input":[],"previous_response_id":"r1"}`},
		{"no input", `{"model":"gpt-5.5","stream":true}`},
		{"non-array input", `{"model":"gpt-5.5","stream":true,"input":{"a":1}}`},
		{"null input", `{"model":"gpt-5.5","stream":true,"input":null}`},
		{"bad json body", `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := rpcModelRouteRequest{
				ModelRouteRequest: pluginapi.ModelRouteRequest{
					RequestedModel: "gpt-5.5", SourceFormat: "openai-response",
					Stream: true, Body: []byte(tt.body),
				},
			}
			raw, _ := json.Marshal(req)
			result, err := routeModel(raw)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			var resp struct {
				OK   bool                         `json:"ok"`
				Data pluginapi.ModelRouteResponse `json:"result"`
			}
			if err := json.Unmarshal(result, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !resp.OK {
				t.Error("envelope should be OK")
			}
			if resp.Data.Handled {
				t.Errorf("should decline for %s", tt.name)
			}
		})
	}
}

func TestRouteModelDeclinesNonMatching(t *testing.T) {
	tests := []struct {
		name, model, format string
		stream              bool
	}{
		{"wrong model", "gpt-4o", "openai-response", true},
		{"unsupported format", "gpt-5.5", "gemini", true},
		{"non-stream", "gpt-5.5", "openai-response", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := rpcModelRouteRequest{
				ModelRouteRequest: pluginapi.ModelRouteRequest{
					RequestedModel: tt.model, SourceFormat: tt.format,
					Stream: tt.stream, Body: []byte(`{"input":[]}`),
				},
			}
			raw, _ := json.Marshal(req)
			result, _ := routeModel(raw)
			var resp struct {
				Data pluginapi.ModelRouteResponse `json:"result"`
			}
			json.Unmarshal(result, &resp)
			if resp.Data.Handled {
				t.Errorf("should decline for %s", tt.name)
			}
		})
	}
}

func TestRouteModelAccepts(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	req := rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			RequestedModel: "gpt-5.5", SourceFormat: "openai-response",
			Stream: true, Body: []byte(body),
		},
	}
	raw, _ := json.Marshal(req)
	result, err := routeModel(raw)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var resp struct {
		OK   bool                         `json:"ok"`
		Data pluginapi.ModelRouteResponse `json:"result"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.OK {
		t.Error("envelope should be OK")
	}
	if !resp.Data.Handled {
		t.Error("should accept")
	}
	if resp.Data.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Error("should target self")
	}
	if resp.Data.Reason != "codexcomp_gpt55_truncation_fold" {
		t.Error("reason mismatch")
	}
}

func TestRouteModelAcceptsOpenAIChat(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			RequestedModel: "gpt-5.5", SourceFormat: "openai",
			Stream: true, Body: []byte(body),
		},
	}
	raw, _ := json.Marshal(req)
	result, _ := routeModel(raw)
	var resp struct {
		Data pluginapi.ModelRouteResponse `json:"result"`
	}
	json.Unmarshal(result, &resp)
	if !resp.Data.Handled {
		t.Error("should accept openai (chat completions) for gpt-5.5")
	}
}

func TestRouteModelAcceptsClaude(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			RequestedModel: "gpt-5.5", SourceFormat: "claude",
			Stream: true, Body: []byte(body),
		},
	}
	raw, _ := json.Marshal(req)
	result, _ := routeModel(raw)
	var resp struct {
		Data pluginapi.ModelRouteResponse `json:"result"`
	}
	json.Unmarshal(result, &resp)
	if !resp.Data.Handled {
		t.Error("should accept claude for gpt-5.5")
	}
}

func TestExtractSessionIDHeaderPriority(t *testing.T) {
	headers := http.Header{}
	headers.Set(cpaSessionHeader, " cpa-session ")
	headers.Set(codexCompSessionHeader, "codexcomp-session")
	headers.Set(claudeCodeSessionHeader, "claude-session")
	req := rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Headers:         headers,
			OriginalRequest: []byte(`{"metadata":{"user_id":"body-session"}}`),
		},
	}

	if got := extractSessionID(req); got != "cpa-session" {
		t.Fatalf("extractSessionID() = %q, want cpa-session", got)
	}
}

func TestExtractSessionIDHeaderFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		want   string
	}{
		{
			name:   "codexcomp header",
			header: codexCompSessionHeader,
			value:  "codexcomp-session",
			want:   "codexcomp-session",
		},
		{
			name:   "legacy claude code header",
			header: claudeCodeSessionHeader,
			value:  "claude-session",
			want:   "claude-session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(tt.header, tt.value)
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Headers: headers}}
			if got := extractSessionID(req); got != tt.want {
				t.Fatalf("extractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSessionIDMetadataFallback(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "plain user_id without session marker is ignored",
			body: `{"metadata":{"user_id":"plain-user"}}`,
			want: "",
		},
		{
			name: "json encoded session_id",
			body: `{"metadata":{"user_id":"{\"session_id\":\"json-session\"}"}}`,
			want: "json-session",
		},
		{
			name: "claude code suffix",
			body: `{"metadata":{"user_id":"user_session_suffix-session"}}`,
			want: "suffix-session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{OriginalRequest: []byte(tt.body)}}
			if got := extractSessionID(req); got != tt.want {
				t.Fatalf("extractSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }
