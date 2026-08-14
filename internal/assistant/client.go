package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// Providers answer with well under a megabyte; a misconfigured endpoint
	// (a proxy error page, a model listing) must not be read without bound.
	maxResponseBytes = 1 << 20
	maxErrorChars    = 300
	// A hosted tool that has to be echoed back costs one extra round trip per
	// call. Three rounds cover a search plus a follow-up without letting a
	// looping model spend the whole request timeout.
	maxToolRounds = 3
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// The remaining fields only appear while a tool call is being answered.
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ToolCall is one tool invocation the model asked for.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name string `json:"name"`
	// Arguments is JSON text. Providers agree on a string here, but a few
	// gateways send the object itself, so it is decoded leniently and always
	// sent back as a string.
	Arguments jsonText `json:"arguments"`
}

// jsonText holds JSON that arrives either quoted as a string or as the value.
type jsonText string

func (t *jsonText) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*t = jsonText(text)
		return nil
	}
	*t = jsonText(data)
	return nil
}

func (t jsonText) MarshalJSON() ([]byte, error) { return json.Marshal(string(t)) }

type Request struct {
	Config      Config
	Messages    []Message
	MaxTokens   int
	Temperature float64
	// Tools are the provider-hosted tools to declare, already resolved for this
	// endpoint by ActiveTools.
	Tools []Tool
}

// Reply is one finished completion.
type Reply struct {
	Content string
	// ToolsDropped records that the endpoint rejected the hosted tools and the
	// answer came from a retry without them.
	ToolsDropped bool
}

// Completer performs one chat completion. The manager holds this interface so
// tests can drive summaries and answers without a network endpoint.
type Completer interface {
	Complete(ctx context.Context, request Request) (Reply, error)
}

// HTTPCompleter calls an OpenAI-compatible /chat/completions endpoint.
type HTTPCompleter struct {
	Client *http.Client
}

func (h *HTTPCompleter) Complete(ctx context.Context, request Request) (Reply, error) {
	endpoint, err := CompletionURL(request.Config.Endpoint)
	if err != nil {
		return Reply{}, err
	}
	if request.Config.Model == "" {
		return Reply{}, errors.New("请先填写模型名称")
	}

	tools := request.Tools
	messages := request.Messages
	dropped := false
	for round := 0; round <= maxToolRounds; round++ {
		body, err := json.Marshal(buildPayload(request, messages, tools))
		if err != nil {
			return Reply{}, fmt.Errorf("编码模型请求失败：%w", err)
		}
		response, err := h.post(ctx, endpoint, request.Config.APIKey, body)
		if err != nil {
			return Reply{}, err
		}
		if response.status != http.StatusOK {
			// An endpoint that refuses the tool declaration can still answer the
			// question, and an answer without search beats an error on screen.
			// Retried only once, so a failure unrelated to tools still surfaces.
			if len(tools) > 0 && !dropped && rejectsDeclaredTools(response.status) {
				tools, messages, dropped = nil, request.Messages, true
				continue
			}
			return Reply{}, fmt.Errorf("%s 返回 %s：%s",
				DisplayEndpoint(endpoint), response.label, describeError(response.data))
		}
		decoded, err := decodeCompletion(response.data)
		if err != nil {
			return Reply{}, err
		}
		if len(decoded.ToolCalls) > 0 {
			if echoes, ok := echoToolResults(decoded.ToolCalls); ok && round < maxToolRounds {
				messages = append(append(append([]Message(nil), messages...), Message{
					Role: RoleAssistant, Content: decoded.Content, ToolCalls: decoded.ToolCalls,
				}), echoes...)
				continue
			}
			// The model asked for a tool this client cannot run. Dropping the
			// declaration makes it answer from its own knowledge instead.
			if decoded.Content == "" && len(tools) > 0 && !dropped {
				tools, messages, dropped = nil, request.Messages, true
				continue
			}
		}
		if decoded.Content == "" {
			return Reply{}, errors.New("模型没有返回内容")
		}
		return Reply{Content: decoded.Content, ToolsDropped: dropped}, nil
	}
	return Reply{}, errors.New("模型反复请求调用内置工具，没有给出回答")
}

type httpReply struct {
	status int
	label  string
	data   []byte
}

func (h *HTTPCompleter) post(ctx context.Context, endpoint, apiKey string, body []byte) (httpReply, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return httpReply{}, fmt.Errorf("构造模型请求失败：%w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return httpReply{}, fmt.Errorf("请求 %s 超时或被取消", DisplayEndpoint(endpoint))
		}
		return httpReply{}, fmt.Errorf("无法连接 %s：%w", DisplayEndpoint(endpoint), err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return httpReply{}, fmt.Errorf("读取模型响应失败：%w", err)
	}
	return httpReply{status: response.StatusCode, label: response.Status, data: data}, nil
}

func buildPayload(request Request, messages []Message, tools []Tool) map[string]any {
	payload := map[string]any{
		"model":       request.Config.Model,
		"messages":    messages,
		"stream":      false,
		"temperature": request.Temperature,
	}
	if request.MaxTokens > 0 {
		payload["max_tokens"] = request.MaxTokens
	}
	declarations := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if len(tool.Spec) > 0 {
			declarations = append(declarations, tool.Spec)
		}
		for key, value := range tool.Params {
			mergeRequestField(payload, key, value)
		}
	}
	if len(declarations) > 0 {
		// tool_choice stays unset: "auto" is already the default wherever tools
		// are declared, and some providers reject it next to a hosted tool.
		payload["tools"] = declarations
	}
	return payload
}

// mergeRequestField deep-merges one vendor request field into the payload so
// that two tools extending the same envelope do not overwrite each other.
// Values are cloned on the way in because the catalog is package state shared
// by every request.
func mergeRequestField(payload map[string]any, key string, value any) {
	incoming, isMap := value.(map[string]any)
	existing, wasMap := payload[key].(map[string]any)
	if !isMap || !wasMap {
		payload[key] = cloneToolValue(value)
		return
	}
	for name, item := range incoming {
		mergeRequestField(existing, name, item)
	}
}

func cloneToolValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, item := range typed {
			clone[key] = cloneToolValue(item)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = cloneToolValue(item)
		}
		return clone
	default:
		return typed
	}
}

// echoToolResults answers the hosted tools that expect their own call back.
// Moonshot defines its builtin functions this way: the model writes the search
// arguments and returning them verbatim is what makes Moonshot run the search.
// Every call has to be answerable, because a plain function call is one KSpeech
// never declared and has no way to run.
func echoToolResults(calls []ToolCall) ([]Message, bool) {
	messages := make([]Message, 0, len(calls))
	for _, call := range calls {
		if call.Type != "builtin_function" && !strings.HasPrefix(call.Function.Name, "$") {
			return nil, false
		}
		arguments := strings.TrimSpace(string(call.Function.Arguments))
		if arguments == "" {
			arguments = "{}"
		}
		messages = append(messages, Message{
			Role:       RoleTool,
			Content:    arguments,
			Name:       call.Function.Name,
			ToolCallID: call.ID,
		})
	}
	return messages, len(messages) > 0
}

// rejectsDeclaredTools reports the statuses a wrong tool declaration produces.
// Authentication, quota and server failures are deliberately absent: dropping
// the tools would not change their outcome.
func rejectsDeclaredTools(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

type completion struct {
	Content   string
	ToolCalls []ToolCall
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []ToolCall      `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

func decodeCompletion(data []byte) (completion, error) {
	var decoded completionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return completion{}, fmt.Errorf("模型响应不是预期的 JSON：%s", truncate(string(data), maxErrorChars))
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return completion{}, errors.New("模型返回错误：" + describeError(data))
	}
	if len(decoded.Choices) == 0 {
		return completion{}, fmt.Errorf("模型响应没有 choices：%s", truncate(string(data), maxErrorChars))
	}
	message := decoded.Choices[0].Message
	content, err := decodeContent(message.Content)
	if err != nil {
		return completion{}, err
	}
	content = strings.TrimSpace(stripThinking(content))
	if content == "" && len(message.ToolCalls) == 0 {
		// Reasoning models sometimes place a short answer in the thinking
		// channel only; using it beats reporting an empty reply.
		content = strings.TrimSpace(message.ReasoningContent)
	}
	return completion{Content: content, ToolCalls: message.ToolCalls}, nil
}

// decodeContent accepts both the plain string form and the segmented form that
// some OpenAI-compatible gateways return.
func decodeContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("无法解析模型返回的内容：%s", truncate(string(raw), maxErrorChars))
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String(), nil
}

// stripThinking removes the <think> block that reasoning models emit before
// the answer.
func stripThinking(content string) string {
	for {
		start := strings.Index(content, "<think>")
		if start < 0 {
			return content
		}
		end := strings.Index(content[start:], "</think>")
		if end < 0 {
			// The block never closed: everything after it is thinking.
			return content[:start]
		}
		content = content[:start] + content[start+end+len("</think>"):]
	}
}

func describeError(data []byte) string {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil {
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var message string
			if err := json.Unmarshal(envelope.Error, &message); err == nil && message != "" {
				return truncate(message, maxErrorChars)
			}
			var detail struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			}
			if err := json.Unmarshal(envelope.Error, &detail); err == nil && detail.Message != "" {
				return truncate(detail.Message, maxErrorChars)
			}
		}
		if envelope.Message != "" {
			return truncate(envelope.Message, maxErrorChars)
		}
	}
	return truncate(strings.TrimSpace(string(data)), maxErrorChars)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "（无响应内容）"
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
