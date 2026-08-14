package assistant

import (
	"net/url"
	"strings"
)

// Tool is one provider-hosted tool. The provider runs it inside the same
// request, so KSpeech only declares it and reads the finished answer. Moonshot
// is the one exception: it asks the client to echo the call back, which is what
// Echo marks.
//
// Only tools that work over plain OpenAI-compatible /chat/completions belong
// here. Several vendors ship richer tools on their own Responses API; those are
// unreachable from KSpeech and are recorded as a Provider.Note instead of a
// declaration the endpoint would reject.
type Tool struct {
	ID    string
	Label string
	// Spec is appended to the request "tools" array.
	Spec map[string]any
	// Params is merged into the top level of the request body, for vendors that
	// switch search on with a request field rather than a tool declaration.
	Params map[string]any
	// Echo marks a tool whose call the client must return verbatim as a tool
	// message before the provider will run it.
	Echo bool
}

// Provider is the vendor behind an OpenAI-compatible endpoint. The vendor is
// what decides the shape of a hosted tool, so it has to be recognized before
// anything can be attached.
type Provider struct {
	ID    string
	Name  string
	Tools []Tool
	// Note is shown on the settings page: either why Tools is empty, or what to
	// watch out for when it is not.
	Note string
}

// Supported reports whether this provider offers any hosted tool over
// /chat/completions.
func (p Provider) Supported() bool { return len(p.Tools) > 0 }

type providerRule struct {
	provider Provider
	// hosts match the endpoint's hostname, either exactly or as a parent
	// domain. A recognized host is authoritative: it beats every model marker.
	hosts []string
	// models match a substring of the model name. Relay gateways such as
	// one-api and new-api sit on a private domain and forward to the real
	// vendor, so the model name is all that is left to go on.
	models []string
}

const (
	webSearchLabel = "联网搜索"
	// Deep-merged into a request body, this key carries the vendor extensions
	// that the Gemini OpenAI-compatible layer accepts.
	geminiExtraBodyKey = "extra_body"
)

// providerRules is ordered: the first rule whose host or model matches wins.
// Vendors with distinctive names come before ones whose markers are broad.
var providerRules = []providerRule{
	{
		provider: Provider{
			ID:   "openai",
			Name: "OpenAI",
			Tools: []Tool{{
				ID:    "openai.web_search",
				Label: webSearchLabel,
				Spec:  map[string]any{"type": "web_search"},
			}},
		},
		hosts:  []string{"api.openai.com"},
		models: []string{"gpt-", "chatgpt", "o1-", "o3-", "o4-"},
	},
	{
		provider: Provider{
			ID:   "zhipu",
			Name: "智谱 GLM",
			Tools: []Tool{{
				ID:    "zhipu.web_search",
				Label: webSearchLabel,
				Spec: map[string]any{
					"type": "web_search",
					"web_search": map[string]any{
						"enable": true,
						// search_std is the basic engine; the pro engines cost
						// more per call and a caption assistant asks often.
						"search_engine": "search_std",
						"search_result": true,
					},
				},
			}},
		},
		hosts:  []string{"open.bigmodel.cn", "bigmodel.cn", "api.z.ai"},
		models: []string{"glm-", "charglm", "codegeex"},
	},
	{
		provider: Provider{
			ID:   "moonshot",
			Name: "Kimi（Moonshot）",
			Tools: []Tool{{
				ID:    "moonshot.web_search",
				Label: webSearchLabel,
				// Moonshot's builtin functions carry a "$" prefix and take no
				// JSON schema: the model writes the arguments and Moonshot runs
				// the search once the client echoes them back.
				Spec: map[string]any{
					"type":     "builtin_function",
					"function": map[string]any{"name": "$web_search"},
				},
				Echo: true,
			}},
		},
		hosts:  []string{"api.moonshot.cn", "api.moonshot.ai", "api.kimi.com", "api.kimi.ai"},
		models: []string{"kimi-", "moonshot-"},
	},
	{
		provider: Provider{
			ID:   "dashscope",
			Name: "通义千问（阿里云百炼）",
			Tools: []Tool{{
				ID:     "dashscope.web_search",
				Label:  webSearchLabel,
				Params: map[string]any{"enable_search": true},
			}},
		},
		hosts:  []string{"dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com", "maas.aliyuncs.com"},
		models: []string{"qwen", "qwq-", "qvq-", "tongyi"},
	},
	{
		provider: Provider{
			ID:   "baidu",
			Name: "文心一言（百度千帆）",
			Tools: []Tool{{
				ID:    "baidu.web_search",
				Label: webSearchLabel,
				Params: map[string]any{
					"web_search": map[string]any{"enable": true, "search_mode": "auto"},
				},
			}},
		},
		hosts:  []string{"qianfan.baidubce.com", "aip.baidubce.com"},
		models: []string{"ernie"},
	},
	{
		provider: Provider{
			ID:   "tencent",
			Name: "腾讯混元",
			Tools: []Tool{{
				ID:     "tencent.web_search",
				Label:  webSearchLabel,
				Params: map[string]any{"enable_enhancement": true},
			}},
		},
		hosts:  []string{"api.hunyuan.cloud.tencent.com", "hunyuan.tencentcloudapi.com"},
		models: []string{"hunyuan"},
	},
	{
		provider: Provider{
			ID:   "minimax",
			Name: "MiniMax",
			Tools: []Tool{{
				ID:    "minimax.web_search",
				Label: webSearchLabel,
				Spec:  map[string]any{"type": "web_search"},
			}},
		},
		hosts:  []string{"api.minimax.chat", "api.minimaxi.com", "api.minimax.io"},
		models: []string{"minimax-", "abab"},
	},
	{
		provider: Provider{
			ID:   "google",
			Name: "Google Gemini",
			Tools: []Tool{{
				ID:    "google.google_search",
				Label: webSearchLabel,
				// The compatibility layer refuses google_search inside "tools"
				// and only reads Gemini extensions from this envelope.
				Params: map[string]any{
					geminiExtraBodyKey: map[string]any{
						"google": map[string]any{"google_search": map[string]any{}},
					},
				},
			}},
		},
		hosts:  []string{"generativelanguage.googleapis.com", "aiplatform.googleapis.com"},
		models: []string{"gemini-"},
	},
	{
		provider: Provider{
			ID:   "openrouter",
			Name: "OpenRouter",
			Note: "OpenRouter 的 web 插件由平台执行，每次回答都会搜索并按结果计费（默认 5 条）。",
			Tools: []Tool{{
				ID:    "openrouter.web",
				Label: webSearchLabel,
				// OpenRouter bills per web result, so keep the request at the
				// documented default instead of widening it.
				Params: map[string]any{
					"plugins": []any{map[string]any{"id": "web", "max_results": 5}},
				},
			}},
		},
		// The model slug alone is not enough: every relay borrows OpenRouter's
		// "vendor/model" naming, and the web plugin only exists here.
		hosts: []string{"openrouter.ai"},
	},
	{
		provider: Provider{
			ID:   "deepseek",
			Name: "DeepSeek",
			Note: "DeepSeek 官方接口没有提供托管工具，联网搜索需要自己接入搜索服务。",
		},
		hosts:  []string{"api.deepseek.com"},
		models: []string{"deepseek"},
	},
	{
		provider: Provider{
			ID:   "volcengine",
			Name: "豆包（火山方舟）",
			Note: "火山方舟的联网内容插件走 Responses API 或绑定插件的 Bot 端点，/chat/completions 上无法声明。",
		},
		hosts:  []string{"ark.cn-beijing.volces.com", "volces.com"},
		models: []string{"doubao", "ep-2"},
	},
	{
		provider: Provider{
			ID:   "xai",
			Name: "xAI Grok",
			Note: "xAI 的 web_search、x_search 只在 Responses API 上提供，/chat/completions 不接受。",
		},
		hosts:  []string{"api.x.ai"},
		models: []string{"grok-"},
	},
	{
		provider: Provider{
			ID:   "anthropic",
			Name: "Anthropic Claude",
			Note: "Anthropic 的托管工具只在原生 Messages 接口上提供，OpenAI 兼容端点会拒绝。",
		},
		hosts:  []string{"api.anthropic.com"},
		models: []string{"claude-"},
	},
}

var localProvider = Provider{
	ID:   "local",
	Name: "本机或内网模型服务",
	Note: "本机模型服务没有托管工具；要联网可以在模型侧自行接入搜索。",
}

// DetectProvider identifies the vendor behind an endpoint. The host decides
// when it is one KSpeech knows; otherwise the model name does, because a relay
// gateway hides the vendor behind a private domain.
func DetectProvider(endpoint, model string) (Provider, bool) {
	host := endpointHost(endpoint)
	if host != "" {
		for _, rule := range providerRules {
			if matchesHost(host, rule.hosts) {
				return rule.provider, true
			}
		}
		// A local server answers for whatever model it has loaded, so a model
		// marker here would name a vendor that is not actually being called.
		if isLocalHost(host) {
			return localProvider, true
		}
	}
	if key := strings.ToLower(strings.TrimSpace(model)); key != "" {
		for _, rule := range providerRules {
			for _, marker := range rule.models {
				if strings.Contains(key, marker) {
					return rule.provider, true
				}
			}
		}
	}
	return Provider{}, false
}

// ActiveTools returns the hosted tools to declare for one resolved config.
func ActiveTools(cfg Config) []Tool {
	if !cfg.Tools {
		return nil
	}
	provider, ok := DetectProvider(cfg.Endpoint, cfg.Model)
	if !ok {
		return nil
	}
	return append([]Tool(nil), provider.Tools...)
}

// ToolStatus is what the settings page shows about the current endpoint: which
// hosted tools an answer will carry, and what to know about them.
type ToolStatus struct {
	Provider string
	Tools    []string
	Note     string
}

// DescribeTools explains the effect of the current settings.
func DescribeTools(cfg Config) ToolStatus {
	status := ToolStatus{Tools: make([]string, 0, 2)}
	if !cfg.Tools {
		// The switch itself already says this; a note here would only repeat it
		// on every screen that shows one.
		return status
	}
	if cfg.Endpoint == "" && cfg.Model == "" {
		// Nothing to report before the endpoint has been filled in.
		return status
	}
	provider, ok := DetectProvider(cfg.Endpoint, cfg.Model)
	if !ok {
		status.Note = "无法从 API 地址和模型名称判断服务商，不会声明内置工具。"
		return status
	}
	status.Provider = provider.Name
	status.Note = provider.Note
	for _, tool := range provider.Tools {
		status.Tools = append(status.Tools, tool.Label)
	}
	return status
}

func endpointHost(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func matchesHost(host string, candidates []string) bool {
	for _, candidate := range candidates {
		if host == candidate || strings.HasSuffix(host, "."+candidate) {
			return true
		}
	}
	return false
}
