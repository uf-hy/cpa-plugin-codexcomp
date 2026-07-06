package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostModelStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}

type hostModelStreamReadResponse struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

type streamChunkOutput struct {
	Payload []byte `json:"payload"`
}

type streamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload"`
	Error    string `json:"error,omitempty"`
}

type streamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func emitChunk(streamID string, payload []byte) {
	if streamID == "" || len(payload) == 0 {
		return
	}
	_, _ = callHost(pluginabi.MethodHostStreamEmit, streamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
}

func closeStream(streamID, errMsg string) {
	if streamID == "" {
		return
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, streamCloseRequest{StreamID: streamID, Error: errMsg})
}

type streamResponseOutput struct {
	Headers http.Header         `json:"headers,omitempty"`
	Chunks  []streamChunkOutput `json:"chunks,omitempty"`
}

// routeModel decides whether to intercept this request.
// We only handle gpt-5.5 streaming Responses API requests.
func routeModel(raw []byte) ([]byte, error) {
	var req rpcModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode model.route request: %w", err)
	}

	pluginLog("info", fmt.Sprintf("routeModel: model=%s source=%s stream=%v", req.RequestedModel, req.SourceFormat, req.Stream))

	if req.RequestedModel != "gpt-5.5" {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if req.SourceFormat != "openai-response" {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}

	pluginLog("info", "routeModel: handling gpt-5.5 responses request")
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "codexcomp_gpt55_truncation_fold",
	})
}

// execute handles non-streaming requests by falling back to host.model.execute.
func execute(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.execute request: %w", err)
	}

	body := req.OriginalRequest
	if len(req.Payload) > 0 {
		body = req.Payload
	}

	result, err := callHost(pluginabi.MethodHostModelExecute, hostModelExecRequest{
		EntryProtocol:  req.SourceFormat,
		ExitProtocol:   req.SourceFormat,
		Model:          req.Model,
		Stream:         false,
		Body:           body,
		Headers:        cloneHeader(req.Headers),
		Query:          req.Query,
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}

	var resp pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("decode host.model.execute result: %w", err)
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: resp.Body, Headers: resp.Headers})
}

// executeStream is the core fold logic: send the request upstream via
// host.model.execute_stream, read chunks, detect 518n-2 truncation,
// continue if needed, and fold all rounds into one response.
func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.execute_stream request: %w", err)
	}

	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream"), nil
	}

	baseBody := map[string]any{}
	bodyBytes := req.OriginalRequest
	if len(req.Payload) > 0 {
		bodyBytes = req.Payload
	}
	if err := json.Unmarshal(bodyBytes, &baseBody); err != nil {
		closeStream(streamID, "decode request body: "+err.Error())
		return nil, fmt.Errorf("decode request body: %w", err)
	}

	origInput, _ := baseBody["input"].([]any)
	if origInput == nil {
		origInput = []any{}
	} else {
		origInput = append([]any(nil), origInput...)
	}

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				closeStream(streamID, fmt.Sprintf("fold panic: %v", recovered))
			}
		}()
		runFold(baseBody, origInput, req, streamID)
		closeStream(streamID, "")
	}()

	return okEnvelope(map[string]any{
		"headers": http.Header{"Content-Type": []string{"text/event-stream"}},
	})
}

func runFold(baseBody map[string]any, origInput []any, req rpcExecutorRequest, streamID string) {
	foldState := newFoldState(baseBody, origInput, req, req.HostCallbackID)

	for {
		roundChunks, _, terminal, usage, roundErr := foldState.openRound()
		if roundErr != nil {
			if foldState.roundNo == 0 {
				emitChunk(streamID, sseEvent(failedEvent(502, roundErr.Error())))
				return
			}
			emitChunk(streamID, sseEvent(foldState.incompleteEvent("upstream_error")))
			return
		}

		for _, ch := range roundChunks {
			emitChunk(streamID, ch.Payload)
		}

		if terminal == nil {
			emitChunk(streamID, sseEvent(foldState.incompleteEvent("upstream_eof")))
			return
		}

		foldState.endRound(terminal, usage)

		if foldState.shouldContinue() {
			if err := foldState.prepareNextRound(); err != nil {
				emitChunk(streamID, sseEvent(foldState.incompleteEvent("upstream_error")))
				return
			}
			continue
		}

		flushChunks := foldState.flushCleanStop()
		for _, ch := range flushChunks {
			emitChunk(streamID, ch.Payload)
		}
		emitChunk(streamID, sseEvent(foldState.terminalEvent()))
		return
	}
}

// sseEvent serializes an event dict as an SSE data line.
func sseEvent(ev map[string]any) []byte {
	raw, _ := json.Marshal(ev)
	return append([]byte("data: "), append(raw, '\n', '\n')...)
}

// foldState tracks the state across multiple rounds of the fold.
type foldState struct {
	baseBody      map[string]any
	origInput     []any
	req           rpcExecutorRequest
	hostCallbackID string

	roundNo        int
	dsOI           int
	seq            int
	baseResponse   map[string]any
	finalOutput    []map[string]any
	replayTail     []any
	summedUsage    map[string]any
	firstUsage     map[string]any
	roundsInfo     []map[string]any

	// per-round state
	roundReasoning []map[string]any
	kind           map[int]string
	oiToDS         map[int]int
	buffered       []bufferedEntry
	terminal       map[string]any
	usage          map[string]any
}

type bufferedEntry struct {
	oi     int
	item   map[string]any
	events []map[string]any
}

func newFoldState(baseBody map[string]any, origInput []any, req rpcExecutorRequest, hostCallbackID string) *foldState {
	return &foldState{
		baseBody:       baseBody,
		origInput:      origInput,
		req:            req,
		hostCallbackID: hostCallbackID,
		summedUsage:    map[string]any{},
		kind:           map[int]string{},
		oiToDS:         map[int]int{},
	}
}

// openRound sends the request upstream and reads all chunks.
// Returns: downstream chunks, headers, terminal event, usage, error.
func (fs *foldState) openRound() ([]streamChunkOutput, http.Header, map[string]any, map[string]any, error) {
	fs.roundNo++
	fs.roundReasoning = nil
	fs.kind = map[int]string{}
	fs.oiToDS = map[int]int{}
	fs.buffered = nil
	fs.terminal = nil
	fs.usage = nil

	var bodyBytes []byte
	var err error
	if fs.roundNo == 1 {
		bodyBytes, err = json.Marshal(fs.baseBody)
	} else {
		bodyBytes, err = json.Marshal(nextRoundBody(fs.baseBody, append(fs.origInput, fs.replayTail...)))
	}
	if err != nil {
		return nil, nil, nil, nil, err
	}

	result, err := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecRequest{
		EntryProtocol:  fs.req.SourceFormat,
		ExitProtocol:   fs.req.SourceFormat,
		Model:          fs.req.Model,
		Stream:         true,
		Body:           bodyBytes,
		Headers:        cloneHeader(fs.req.Headers),
		Query:          fs.req.Query,
		HostCallbackID: fs.hostCallbackID,
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}

	var streamResp hostModelStreamResponse
	if err := json.Unmarshal(result, &streamResp); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("decode host.model.execute_stream result: %w", err)
	}
	if streamResp.StreamID == "" {
		return nil, nil, nil, nil, fmt.Errorf("host.model.execute_stream returned empty stream_id")
	}
	defer func() {
		_, _ = callHost(pluginabi.MethodHostModelStreamClose, map[string]any{"stream_id": streamResp.StreamID})
	}()

	var chunks []streamChunkOutput
	for {
		readResult, err := callHost(pluginabi.MethodHostModelStreamRead, map[string]any{"stream_id": streamResp.StreamID})
		if err != nil {
			return chunks, streamResp.Headers, nil, nil, err
		}
		var readResp hostModelStreamReadResponse
		if err := json.Unmarshal(readResult, &readResp); err != nil {
			return chunks, streamResp.Headers, nil, nil, err
		}
		if len(readResp.Payload) > 0 {
			processed, term := fs.processChunk(readResp.Payload)
			chunks = append(chunks, processed...)
			if term != nil {
				return chunks, streamResp.Headers, term, fs.usage, nil
			}
		}
		if readResp.Error != "" {
			return chunks, streamResp.Headers, nil, nil, fmt.Errorf("%s", readResp.Error)
		}
		if readResp.Done {
			return chunks, streamResp.Headers, fs.terminal, fs.usage, nil
		}
	}
}

// processChunk parses SSE events from a raw payload and classifies them.
// Returns downstream chunks to emit and a terminal event if found.
func (fs *foldState) processChunk(payload []byte) ([]streamChunkOutput, map[string]any) {
	var chunks []streamChunkOutput
	events := parseSSEEvents(payload)
	for _, ev := range events {
		etype, _ := ev["type"].(string)

		if etype == "response.created" || etype == "response.in_progress" {
			if fs.roundNo == 1 {
				if etype == "response.created" {
					if r, ok := ev["response"].(map[string]any); ok {
						fs.baseResponse = r
					}
				}
				fs.stamp(ev)
				raw, _ := json.Marshal(ev)
				chunks = append(chunks, streamChunkOutput{Payload: append([]byte("data: "), append(raw, '\n', '\n')...)})
			}
			continue
		}

		if terminalTypes[etype] {
			fs.terminal = ev
			if r, ok := ev["response"].(map[string]any); ok {
				if u, ok := r["usage"].(map[string]any); ok {
					fs.usage = u
				}
			}
			return chunks, ev
		}

		oi := -1
		if v, ok := ev["output_index"].(float64); ok {
			oi = int(v)
		}

		if etype == "response.output_item.added" {
			item, _ := ev["item"].(map[string]any)
			if item == nil {
				item = map[string]any{}
			}
			itemType, _ := item["type"].(string)
			if itemType == "reasoning" {
				fs.kind[oi] = "reasoning"
				fs.oiToDS[oi] = fs.dsOI
				ev["output_index"] = fs.dsOI
				fs.dsOI++
				fs.stamp(ev)
				raw, _ := json.Marshal(ev)
				chunks = append(chunks, streamChunkOutput{Payload: append([]byte("data: "), append(raw, '\n', '\n')...)})
			} else {
				fs.kind[oi] = "buffered"
				fs.buffered = append(fs.buffered, bufferedEntry{oi: oi, item: item, events: []map[string]any{ev}})
			}
			continue
		}

		k := fs.kind[oi]
		if k == "reasoning" {
			if ds, ok := fs.oiToDS[oi]; ok {
				ev["output_index"] = ds
			}
			if etype == "response.output_item.done" {
				if item, ok := ev["item"].(map[string]any); ok {
					fs.roundReasoning = append(fs.roundReasoning, item)
					fs.finalOutput = append(fs.finalOutput, item)
				}
			}
			fs.stamp(ev)
			raw, _ := json.Marshal(ev)
			chunks = append(chunks, streamChunkOutput{Payload: append([]byte("data: "), append(raw, '\n', '\n')...)})
		} else if k == "buffered" {
			for i := range fs.buffered {
				if fs.buffered[i].oi == oi {
					fs.buffered[i].events = append(fs.buffered[i].events, ev)
					if etype == "response.output_item.done" {
						if item, ok := ev["item"].(map[string]any); ok {
							fs.buffered[i].item = item
						}
					}
					break
				}
			}
		} else {
			fs.stamp(ev)
			raw, _ := json.Marshal(ev)
			chunks = append(chunks, streamChunkOutput{Payload: append([]byte("data: "), append(raw, '\n', '\n')...)})
		}
	}
	return chunks, nil
}

func (fs *foldState) stamp(ev map[string]any) {
	ev["sequence_number"] = fs.seq
	fs.seq++
}

// endRound processes the end of a round: accumulate usage, check truncation.
func (fs *foldState) endRound(terminal map[string]any, usage map[string]any) {
	fs.terminal = terminal
	fs.usage = usage
	sumUsage(fs.summedUsage, usage)
	if fs.roundNo == 1 {
		fs.firstUsage = usage
	}

	rt := reasoningTokens(usage)
	n := tierN(rt)
	fs.roundsInfo = append(fs.roundsInfo, map[string]any{
		"round":            fs.roundNo,
		"reasoning_tokens": rt,
		"n":                n,
	})
}

// shouldContinue returns true when a continuation round should be opened.
func (fs *foldState) shouldContinue() bool {
	if fs.terminal == nil {
		return false
	}
	rt := reasoningTokens(fs.usage)
	n := tierN(rt)
	if !inContinueWindow(n) {
		return false
	}
	hasEnc := false
	if len(fs.roundReasoning) > 0 {
		if _, ok := fs.roundReasoning[len(fs.roundReasoning)-1]["encrypted_content"]; ok {
			hasEnc = true
		}
	}
	if !hasEnc {
		return false
	}
	return fs.roundNo <= maxContinue
}

// stoppedReason returns the reason the fold stopped, if non-natural.
func (fs *foldState) stoppedReason() string {
	rt := reasoningTokens(fs.usage)
	n := tierN(rt)
	if n == nil {
		return ""
	}
	hasEnc := false
	if len(fs.roundReasoning) > 0 {
		if _, ok := fs.roundReasoning[len(fs.roundReasoning)-1]["encrypted_content"]; ok {
			hasEnc = true
		}
	}
	if !hasEnc {
		return "no_encrypted_content"
	}
	if fs.roundNo > maxContinue {
		return "max_continue"
	}
	return "tier_out_of_window"
}

// prepareNextRound builds the replay tail for the continuation round.
func (fs *foldState) prepareNextRound() error {
	tail := make([]any, 0, len(fs.roundReasoning)+1)
	for _, r := range fs.roundReasoning {
		tail = append(tail, r)
	}
	tail = append(tail, commentaryNudge())
	fs.replayTail = append(fs.replayTail, tail...)
	return nil
}

// flushCleanStop emits the buffered output from the clean round as downstream chunks.
func (fs *foldState) flushCleanStop() []streamChunkOutput {
	var chunks []streamChunkOutput
	for _, entry := range fs.buffered {
		for _, ev := range entry.events {
			if _, ok := ev["output_index"]; ok {
				ev["output_index"] = fs.dsOI
			}
			fs.stamp(ev)
			raw, _ := json.Marshal(ev)
			chunks = append(chunks, streamChunkOutput{Payload: append([]byte("data: "), append(raw, '\n', '\n')...)})
		}
		fs.dsOI++
		fs.finalOutput = append(fs.finalOutput, entry.item)
	}
	return chunks
}

// terminalEvent builds the final terminal event for the fold.
func (fs *foldState) terminalEvent() map[string]any {
	return terminalEvent(
		fs.terminal,
		fs.baseResponse,
		fs.finalOutput,
		agentUsage(fs.firstUsage, fs.summedUsage, fs.usage, true),
		fs.roundsInfo,
		fs.summedUsage,
		fs.stoppedReason(),
		"",
	)
}

// incompleteEvent builds a synthesized degraded-stop terminal event.
func (fs *foldState) incompleteEvent(reason string) map[string]any {
	return terminalEvent(
		nil,
		fs.baseResponse,
		fs.finalOutput,
		agentUsage(fs.firstUsage, fs.summedUsage, fs.usage, false),
		fs.roundsInfo,
		fs.summedUsage,
		reason,
		reason,
	)
}

// parseSSEEvents extracts JSON event dicts from an SSE payload.
func parseSSEEvents(payload []byte) []map[string]any {
	var events []map[string]any
	lines := splitLines(payload)
	for _, line := range lines {
		if len(line) <= 6 {
			continue
		}
		if string(line[:6]) != "data: " {
			continue
		}
		data := line[6:]
		if string(data) == "[DONE]" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
