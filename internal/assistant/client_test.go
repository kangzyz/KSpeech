package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPCompleterSendsOpenAIRequest(t *testing.T) {
	t.Parallel()
	var captured struct {
		path        string
		auth        string
		contentType string
		body        map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		captured.path = request.URL.Path
		captured.auth = request.Header.Get("Authorization")
		captured.contentType = request.Header.Get("Content-Type")
		data, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(data, &captured.body)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  下周三评审  "}}]}`))
	}))
	defer server.Close()

	completer := &HTTPCompleter{Client: server.Client()}
	reply, err := completer.Complete(context.Background(), Request{
		Config:    Config{Endpoint: server.URL + "/v1", APIKey: "sk-test", Model: "deepseek-chat"},
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if reply.Content != "下周三评审" {
		t.Fatalf("reply = %q", reply.Content)
	}
	if captured.path != "/v1/chat/completions" {
		t.Fatalf("request path = %q", captured.path)
	}
	if captured.auth != "Bearer sk-test" || captured.contentType != "application/json" {
		t.Fatalf("headers = %q / %q", captured.auth, captured.contentType)
	}
	if captured.body["model"] != "deepseek-chat" || captured.body["stream"] != false {
		t.Fatalf("request body = %#v", captured.body)
	}
	if captured.body["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens = %#v", captured.body["max_tokens"])
	}
}

func TestHTTPCompleterOmitsAuthorizationWithoutKey(t *testing.T) {
	t.Parallel()
	authorized := "unset"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorized = request.Header.Get("Authorization")
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	completer := &HTTPCompleter{Client: server.Client()}
	if _, err := completer.Complete(context.Background(), Request{
		Config:   Config{Endpoint: server.URL + "/v1", Model: "qwen3"},
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if authorized != "" {
		t.Fatalf("Authorization header = %q, want none for a key-less local server", authorized)
	}
}

func TestHTTPCompleterReportsProviderError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	completer := &HTTPCompleter{Client: server.Client()}
	_, err := completer.Complete(context.Background(), Request{
		Config:   Config{Endpoint: server.URL + "/v1", APIKey: "sk-bad", Model: "gpt-4o-mini"},
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a 401 response was accepted")
	}
	if !strings.Contains(err.Error(), "Incorrect API key provided") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want the provider message and status", err)
	}
	if strings.Contains(err.Error(), "sk-bad") {
		t.Fatalf("error leaked the API key: %v", err)
	}
}

// Moonshot runs its builtin search only after the client returns the call it
// asked for, so the second request has to carry the arguments verbatim.
func TestHTTPCompleterEchoesBuiltinToolCalls(t *testing.T) {
	t.Parallel()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			_, _ = response.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",` +
				`"tool_calls":[{"id":"call-1","type":"builtin_function",` +
				`"function":{"name":"$web_search","arguments":"{\"query\":\"发布会时间\"}"}}]}}]}`))
			return
		}
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"发布会在下周三。"}}]}`))
	}))
	defer server.Close()

	completer := &HTTPCompleter{Client: server.Client()}
	reply, err := completer.Complete(context.Background(), Request{
		Config:   Config{Endpoint: server.URL + "/v1", Model: "kimi-k2"},
		Messages: []Message{{Role: RoleUser, Content: "发布会什么时候"}},
		Tools:    ActiveTools(Config{Tools: true, Endpoint: "https://api.moonshot.cn/v1", Model: "kimi-k2"}),
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if reply.Content != "发布会在下周三。" || reply.ToolsDropped {
		t.Fatalf("reply = %#v", reply)
	}
	if len(bodies) != 2 {
		t.Fatalf("request count = %d, want the call to be echoed back", len(bodies))
	}
	declared, _ := bodies[0]["tools"].([]any)
	if len(declared) != 1 {
		t.Fatalf("first request tools = %#v", bodies[0]["tools"])
	}
	messages, _ := bodies[1]["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("second request messages = %#v", messages)
	}
	echoed, _ := messages[2].(map[string]any)
	if echoed["role"] != "tool" || echoed["tool_call_id"] != "call-1" || echoed["name"] != "$web_search" {
		t.Fatalf("echoed message = %#v", echoed)
	}
	if echoed["content"] != `{"query":"发布会时间"}` {
		t.Fatalf("echoed arguments = %#v, want them returned unchanged", echoed["content"])
	}
}

func TestHTTPCompleterRetriesWithoutRejectedTools(t *testing.T) {
	t.Parallel()
	var withTools []bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		_, declared := body["tools"]
		withTools = append(withTools, declared)
		if declared {
			response.WriteHeader(http.StatusBadRequest)
			_, _ = response.Write([]byte(`{"error":{"message":"tools is not supported"}}`))
			return
		}
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"我不清楚最新情况。"}}]}`))
	}))
	defer server.Close()

	completer := &HTTPCompleter{Client: server.Client()}
	reply, err := completer.Complete(context.Background(), Request{
		Config:   Config{Endpoint: server.URL + "/v1", Model: "glm-4.6"},
		Messages: []Message{{Role: RoleUser, Content: "最新情况如何"}},
		Tools:    ActiveTools(Config{Tools: true, Endpoint: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.6"}),
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if reply.Content != "我不清楚最新情况。" || !reply.ToolsDropped {
		t.Fatalf("reply = %#v, want the answer plus a record that tools were dropped", reply)
	}
	if len(withTools) != 2 || !withTools[0] || withTools[1] {
		t.Fatalf("attempts carried tools = %v, want one with and one without", withTools)
	}
}

func TestHTTPCompleterSurfacesFailuresUnrelatedToTools(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		attempts++
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer server.Close()

	completer := &HTTPCompleter{Client: server.Client()}
	_, err := completer.Complete(context.Background(), Request{
		Config:   Config{Endpoint: server.URL + "/v1", APIKey: "sk-bad", Model: "glm-4.6"},
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    ActiveTools(Config{Tools: true, Endpoint: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.6"}),
	})
	if err == nil {
		t.Fatal("a 401 response was accepted")
	}
	// Dropping the tools cannot fix an authentication failure, so retrying
	// would only spend a second request on the same error.
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no retry", attempts)
	}
}

func TestDecodeCompletionAcceptsSegmentedAndReasoningContent(t *testing.T) {
	t.Parallel()
	segmented := `{"choices":[{"message":{"content":[{"type":"text","text":"要点一"},{"type":"text","text":"，要点二"}]}}]}`
	got, err := decodeCompletion([]byte(segmented))
	if err != nil || got.Content != "要点一，要点二" {
		t.Fatalf("segmented content = %q, err = %v", got.Content, err)
	}

	thinking := `{"choices":[{"message":{"content":"<think>先想一下</think>\n结论是可以上线"}}]}`
	got, err = decodeCompletion([]byte(thinking))
	if err != nil || got.Content != "结论是可以上线" {
		t.Fatalf("thinking content = %q, err = %v", got.Content, err)
	}

	reasoningOnly := `{"choices":[{"message":{"content":"","reasoning_content":"只有思考内容"}}]}`
	got, err = decodeCompletion([]byte(reasoningOnly))
	if err != nil || got.Content != "只有思考内容" {
		t.Fatalf("reasoning content = %q, err = %v", got.Content, err)
	}

	if _, err := decodeCompletion([]byte(`{"choices":[]}`)); err == nil {
		t.Fatal("an empty choices array was accepted")
	}
	if _, err := decodeCompletion([]byte(`<html>bad gateway</html>`)); err == nil {
		t.Fatal("a non-JSON body was accepted")
	}
}
