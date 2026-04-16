package llm

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BedrockProvider implements the Provider interface for AWS Bedrock using the
// Converse Stream API. Authentication uses a long-term Bedrock API key
// (AWS_BEARER_TOKEN_BEDROCK) via "Authorization: Bearer <key>", avoiding the
// need for a full SigV4 signer or the AWS SDK.
//
// Model format: "bedrock/<modelId>", e.g. "bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0".
// The response is an application/vnd.amazon.eventstream binary frame stream;
// parseEventStream below decodes it incrementally.
type BedrockProvider struct {
	apiKey string
	region string
	client *http.Client
}

// NewBedrockProvider creates a new Bedrock provider.
// region defaults to "us-east-1" if empty.
func NewBedrockProvider(apiKey, region string) *BedrockProvider {
	if region == "" {
		region = "us-east-1"
	}
	return &BedrockProvider{
		apiKey: apiKey,
		region: region,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// Name returns "bedrock".
func (b *BedrockProvider) Name() string { return "bedrock" }

// StreamCompletion invokes the Converse Stream endpoint for the given model.
func (b *BedrockProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	body := b.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal bedrock request: %w", err)
	}

	url := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/converse-stream",
		b.region, req.Model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create bedrock request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		headers := NormalizeHeaders(resp.Header)
		retryAfter := ExtractRetryAfterFromHeaders(headers)
		return nil, NewProviderError(resp.StatusCode, string(respBody), retryAfter)
	}

	ch := make(chan StreamEvent, 64)
	go b.processStream(resp.Body, ch)
	return ch, nil
}

// buildRequest converts our CompletionRequest into the Bedrock Converse request shape.
func (b *BedrockProvider) buildRequest(req *CompletionRequest) map[string]any {
	body := map[string]any{}

	if req.SystemPrompt != nil && *req.SystemPrompt != "" {
		body["system"] = []map[string]any{
			{"text": *req.SystemPrompt},
		}
	}

	infConfig := map[string]any{}
	if req.MaxTokens != nil {
		infConfig["maxTokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		infConfig["temperature"] = *req.Temperature
	}
	if len(infConfig) > 0 {
		body["inferenceConfig"] = infConfig
	}

	var messages []map[string]any
	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			// System messages handled via body["system"] above.
			continue
		}
		if role == "tool" {
			// Tool results in Bedrock Converse go in a user message with toolResult blocks.
			role = "user"
		}
		if role != "user" && role != "assistant" {
			role = "user"
		}

		var contentBlocks []map[string]any
		for _, part := range m.Content {
			switch part.Type {
			case "text":
				if part.Text != "" {
					contentBlocks = append(contentBlocks, map[string]any{"text": part.Text})
				}
			case "tool_call":
				var input any
				if part.ArgumentsJSON != "" {
					_ = json.Unmarshal([]byte(part.ArgumentsJSON), &input)
				}
				if input == nil {
					input = map[string]any{}
				}
				contentBlocks = append(contentBlocks, map[string]any{
					"toolUse": map[string]any{
						"toolUseId": part.ToolCallID,
						"name":      part.ToolName,
						"input":     input,
					},
				})
			case "tool_result":
				status := "success"
				if part.IsError {
					status = "error"
				}
				contentBlocks = append(contentBlocks, map[string]any{
					"toolResult": map[string]any{
						"toolUseId": part.ToolCallID,
						"content": []map[string]any{
							{"text": part.Text},
						},
						"status": status,
					},
				})
			}
		}

		if len(contentBlocks) > 0 {
			messages = append(messages, map[string]any{
				"role":    role,
				"content": contentBlocks,
			})
		}
	}
	body["messages"] = messages

	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"toolSpec": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": map[string]any{"json": t.InputSchema},
				},
			})
		}
		body["toolConfig"] = map[string]any{"tools": tools}
	}

	return body
}

// processStream decodes the vnd.amazon.eventstream response and emits StreamEvents.
func (b *BedrockProvider) processStream(body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	var usage UsageInfo
	usage.Provider = "bedrock"

	// Accumulate tool-use input JSON across contentBlockDelta chunks, keyed by block index.
	type pendingTool struct {
		ID   string
		Name string
		Args strings.Builder
	}
	pending := make(map[int]*pendingTool)
	var stopReason string

	dec := newEventStreamDecoder(body)
	for {
		msg, err := dec.next()
		if err != nil {
			if err != io.EOF {
				ch <- StreamEvent{Error: &ErrorEvent{Code: "stream_error", Message: err.Error()}}
			}
			break
		}
		if msg == nil {
			break
		}

		eventType := msg.headers[":event-type"]
		messageType := msg.headers[":message-type"]

		// Exception messages carry error payloads.
		if messageType == "exception" || messageType == "error" {
			ch <- StreamEvent{Error: &ErrorEvent{
				Code:    eventType,
				Message: string(msg.payload),
			}}
			break
		}

		var evt map[string]any
		if len(msg.payload) > 0 {
			if err := json.Unmarshal(msg.payload, &evt); err != nil {
				continue
			}
		}

		switch eventType {
		case "contentBlockStart":
			idx := intField(evt, "contentBlockIndex")
			if start, ok := evt["start"].(map[string]any); ok {
				if tu, ok := start["toolUse"].(map[string]any); ok {
					p := &pendingTool{}
					if v, ok := tu["toolUseId"].(string); ok {
						p.ID = v
					}
					if v, ok := tu["name"].(string); ok {
						p.Name = v
					}
					pending[idx] = p
				}
			}

		case "contentBlockDelta":
			idx := intField(evt, "contentBlockIndex")
			delta, _ := evt["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			if text, ok := delta["text"].(string); ok && text != "" {
				ch <- StreamEvent{Delta: &DeltaEvent{Text: text}}
			}
			if tu, ok := delta["toolUse"].(map[string]any); ok {
				if input, ok := tu["input"].(string); ok {
					p, exists := pending[idx]
					if !exists {
						p = &pendingTool{}
						pending[idx] = p
					}
					p.Args.WriteString(input)
				}
			}

		case "contentBlockStop":
			idx := intField(evt, "contentBlockIndex")
			if p, ok := pending[idx]; ok {
				args := p.Args.String()
				if args == "" {
					args = "{}"
				}
				ch <- StreamEvent{ToolCall: &ToolCallEvent{
					ID:            p.ID,
					Name:          p.Name,
					ArgumentsJSON: args,
				}}
				delete(pending, idx)
			}

		case "messageStop":
			if v, ok := evt["stopReason"].(string); ok {
				stopReason = v
			}

		case "metadata":
			if u, ok := evt["usage"].(map[string]any); ok {
				if v, ok := u["inputTokens"].(float64); ok {
					usage.InputTokens = int(v)
				}
				if v, ok := u["outputTokens"].(float64); ok {
					usage.OutputTokens = int(v)
				}
			}
		}
	}

	fr := "stop"
	switch stopReason {
	case "end_turn", "stop_sequence":
		fr = "stop"
	case "max_tokens":
		fr = "max_tokens"
	case "tool_use":
		fr = "tool_calls"
	}
	ch <- StreamEvent{Complete: &CompleteEvent{FinishReason: fr, Usage: usage}}
}

func intField(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

// ---- Minimal vnd.amazon.eventstream decoder ---------------------------------
//
// Frame layout (big-endian):
//   [totalLength uint32][headersLength uint32][preludeCRC uint32]
//   [headers ...][payload ...][messageCRC uint32]
//
// Header layout:
//   [nameLen uint8][name][valueType uint8][value ...]
//
// We only need :event-type and :message-type (both strings, valueType=7).
// CRCs are ignored — we trust the underlying HTTPS transport for integrity.

type eventStreamMessage struct {
	headers map[string]string
	payload []byte
}

type eventStreamDecoder struct {
	r io.Reader
}

func newEventStreamDecoder(r io.Reader) *eventStreamDecoder {
	return &eventStreamDecoder{r: r}
}

func (d *eventStreamDecoder) next() (*eventStreamMessage, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(d.r, prelude[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, io.EOF
		}
		return nil, err
	}
	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	// prelude CRC at prelude[8:12] — ignored.

	if totalLen < 16 || headersLen > totalLen-16 {
		return nil, fmt.Errorf("eventstream: invalid frame lengths total=%d headers=%d", totalLen, headersLen)
	}

	remaining := totalLen - 12 // headers + payload + 4-byte message CRC
	buf := make([]byte, remaining)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return nil, err
	}

	headersBuf := buf[:headersLen]
	payloadLen := int(remaining) - int(headersLen) - 4 // last 4 bytes are message CRC
	if payloadLen < 0 {
		return nil, fmt.Errorf("eventstream: negative payload length")
	}
	payload := buf[headersLen : int(headersLen)+payloadLen]

	headers, err := parseEventStreamHeaders(headersBuf)
	if err != nil {
		return nil, err
	}
	return &eventStreamMessage{headers: headers, payload: payload}, nil
}

func parseEventStreamHeaders(buf []byte) (map[string]string, error) {
	headers := make(map[string]string)
	for len(buf) > 0 {
		if len(buf) < 1 {
			return nil, fmt.Errorf("eventstream: truncated header name length")
		}
		nameLen := int(buf[0])
		buf = buf[1:]
		if len(buf) < nameLen+1 {
			return nil, fmt.Errorf("eventstream: truncated header name")
		}
		name := string(buf[:nameLen])
		buf = buf[nameLen:]
		valueType := buf[0]
		buf = buf[1:]

		switch valueType {
		case 0: // true
			headers[name] = "true"
		case 1: // false
			headers[name] = "false"
		case 6, 7: // byte-array, string — both use 2-byte length + bytes
			if len(buf) < 2 {
				return nil, fmt.Errorf("eventstream: truncated header value length")
			}
			vlen := int(binary.BigEndian.Uint16(buf[:2]))
			buf = buf[2:]
			if len(buf) < vlen {
				return nil, fmt.Errorf("eventstream: truncated header value")
			}
			headers[name] = string(buf[:vlen])
			buf = buf[vlen:]
		case 2: // int8
			if len(buf) < 1 {
				return nil, fmt.Errorf("eventstream: truncated int8 header")
			}
			buf = buf[1:]
		case 3: // int16
			if len(buf) < 2 {
				return nil, fmt.Errorf("eventstream: truncated int16 header")
			}
			buf = buf[2:]
		case 4: // int32
			if len(buf) < 4 {
				return nil, fmt.Errorf("eventstream: truncated int32 header")
			}
			buf = buf[4:]
		case 5, 8: // int64, timestamp
			if len(buf) < 8 {
				return nil, fmt.Errorf("eventstream: truncated int64/timestamp header")
			}
			buf = buf[8:]
		case 9: // uuid
			if len(buf) < 16 {
				return nil, fmt.Errorf("eventstream: truncated uuid header")
			}
			buf = buf[16:]
		default:
			return nil, fmt.Errorf("eventstream: unknown header value type %d", valueType)
		}
	}
	return headers, nil
}
