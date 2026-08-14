package assistant

import "testing"

func TestDetectProviderPrefersHostOverModelName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     string
	}{
		{"官方主机", "https://open.bigmodel.cn/api/paas/v4", "glm-4-flash", "zhipu"},
		{"子域名", "https://relay.api.moonshot.cn/v1", "kimi-k2", "moonshot"},
		// A gateway forwards to whichever vendor the model belongs to, so the
		// model name is the only thing left that identifies it.
		{"中转网关按模型名", "https://my-relay.example.com/v1", "glm-4.6", "zhipu"},
		{"中转网关未知模型", "https://my-relay.example.com/v1", "my-finetune", ""},
		// The host wins: OpenRouter serves every vendor's models but only its
		// own web plugin works.
		{"聚合平台以主机为准", "https://openrouter.ai/api/v1", "deepseek/deepseek-chat", "openrouter"},
		// A local server answers for whatever it has loaded; naming a vendor
		// from the model would attach a tool nothing there can run.
		{"本机服务", "http://127.0.0.1:11434/v1", "qwen3:8b", "local"},
		{"未填模型", "https://api.deepseek.com/v1", "", "deepseek"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			provider, ok := DetectProvider(testCase.endpoint, testCase.model)
			if testCase.want == "" {
				if ok {
					t.Fatalf("provider = %q, want no match", provider.ID)
				}
				return
			}
			if !ok || provider.ID != testCase.want {
				t.Fatalf("provider = %q (matched %v), want %q", provider.ID, ok, testCase.want)
			}
		})
	}
}

func TestActiveToolsFollowTheSwitchAndTheProvider(t *testing.T) {
	t.Parallel()
	enabled := Config{Tools: true, Endpoint: "https://api.moonshot.cn/v1", Model: "kimi-k2"}
	tools := ActiveTools(enabled)
	if len(tools) != 1 || tools[0].ID != "moonshot.web_search" || !tools[0].Echo {
		t.Fatalf("tools = %#v", tools)
	}

	off := enabled
	off.Tools = false
	if tools := ActiveTools(off); len(tools) != 0 {
		t.Fatalf("a disabled switch still attached %#v", tools)
	}

	// DeepSeek hosts no tools at all: attaching one would fail every request.
	deepseek := Config{Tools: true, Endpoint: "https://api.deepseek.com/v1", Model: "deepseek-chat"}
	if tools := ActiveTools(deepseek); len(tools) != 0 {
		t.Fatalf("deepseek tools = %#v", tools)
	}
	if status := DescribeTools(deepseek); status.Provider != "DeepSeek" || status.Note == "" {
		t.Fatalf("status = %#v, want the provider named and the gap explained", status)
	}
}

// A catalog entry is only exercised once someone points KSpeech at that vendor,
// so a typo would otherwise surface as a failed request on a user's machine.
func TestCatalogEntriesAreWellFormed(t *testing.T) {
	t.Parallel()
	seen := make(map[string]string)
	for _, rule := range providerRules {
		provider := rule.provider
		if provider.ID == "" || provider.Name == "" {
			t.Fatalf("provider %#v is missing an identifier", provider)
		}
		if len(rule.hosts) == 0 {
			t.Fatalf("provider %q has no host to match", provider.ID)
		}
		if !provider.Supported() && provider.Note == "" {
			t.Fatalf("provider %q offers nothing and does not say why", provider.ID)
		}
		for _, tool := range provider.Tools {
			if tool.ID == "" || tool.Label == "" {
				t.Fatalf("provider %q has an unnamed tool: %#v", provider.ID, tool)
			}
			if len(tool.Spec) == 0 && len(tool.Params) == 0 {
				t.Fatalf("tool %q would add nothing to a request", tool.ID)
			}
			if len(tool.Spec) > 0 && tool.Spec["type"] == nil {
				t.Fatalf("tool %q has no type for the tools array", tool.ID)
			}
			if owner, duplicate := seen[tool.ID]; duplicate {
				t.Fatalf("tool id %q is used by both %q and %q", tool.ID, owner, provider.ID)
			}
			seen[tool.ID] = provider.ID
		}
	}
}

func TestBuildPayloadDeclaresToolsAndMergesVendorFields(t *testing.T) {
	t.Parallel()
	request := Request{Config: Config{Model: "glm-4.6"}, Temperature: 0.3, MaxTokens: 800}
	declared := buildPayload(request, nil, []Tool{
		{ID: "zhipu.web_search", Spec: map[string]any{"type": "web_search"}},
		{ID: "vendor.flag", Params: map[string]any{"enable_search": true}},
	})
	tools, ok := declared["tools"].([]map[string]any)
	if !ok || len(tools) != 1 || tools[0]["type"] != "web_search" {
		t.Fatalf("tools = %#v", declared["tools"])
	}
	if declared["enable_search"] != true {
		t.Fatalf("vendor request field = %#v", declared["enable_search"])
	}
	// "auto" is already the default and some providers reject it next to a
	// hosted tool, so it must not be sent.
	if _, present := declared["tool_choice"]; present {
		t.Fatalf("payload = %#v, want no tool_choice", declared)
	}
	if plain := buildPayload(request, nil, nil); plain["tools"] != nil {
		t.Fatalf("a tool-less request declared %#v", plain["tools"])
	}
}

func TestMergeRequestFieldKeepsTheCatalogIntact(t *testing.T) {
	t.Parallel()
	// Two tools extending one envelope must combine rather than overwrite, and
	// neither may write through into the shared catalog map.
	shared := map[string]any{"google": map[string]any{"google_search": map[string]any{}}}
	tools := []Tool{
		{ID: "a", Params: map[string]any{geminiExtraBodyKey: shared}},
		{ID: "b", Params: map[string]any{geminiExtraBodyKey: map[string]any{
			"google": map[string]any{"thinking_config": map[string]any{"include_thoughts": false}},
		}}},
	}
	payload := buildPayload(Request{Config: Config{Model: "gemini-3-pro"}}, nil, tools)
	google, _ := payload[geminiExtraBodyKey].(map[string]any)["google"].(map[string]any)
	if _, ok := google["google_search"]; !ok {
		t.Fatalf("merged envelope = %#v", payload[geminiExtraBodyKey])
	}
	if _, ok := google["thinking_config"]; !ok {
		t.Fatalf("second tool was overwritten: %#v", payload[geminiExtraBodyKey])
	}
	if _, leaked := shared["google"].(map[string]any)["thinking_config"]; leaked {
		t.Fatal("the merge wrote through into the catalog map")
	}
}
