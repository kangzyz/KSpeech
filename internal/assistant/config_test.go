package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/config"
)

func TestCompletionURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		endpoint string
		want     string
		wantErr  string
	}{
		{name: "base url", endpoint: "https://api.deepseek.com/v1", want: "https://api.deepseek.com/v1/chat/completions"},
		{name: "trailing slash", endpoint: "https://api.openai.com/v1/", want: "https://api.openai.com/v1/chat/completions"},
		{name: "already complete", endpoint: "https://gw.example.com/v1/chat/completions", want: "https://gw.example.com/v1/chat/completions"},
		{name: "keeps query", endpoint: "https://host.openai.azure.com/openai/deployments/x?api-version=2024-02-01", want: "https://host.openai.azure.com/openai/deployments/x/chat/completions?api-version=2024-02-01"},
		{name: "local http", endpoint: "http://127.0.0.1:11434/v1", want: "http://127.0.0.1:11434/v1/chat/completions"},
		{name: "intranet http", endpoint: "http://192.168.1.9:8000/v1", want: "http://192.168.1.9:8000/v1/chat/completions"},
		{name: "hostname without dot", endpoint: "http://gpu-server:8000/v1", want: "http://gpu-server:8000/v1/chat/completions"},
		{name: "reject public http", endpoint: "http://api.deepseek.com/v1", wantErr: "https"},
		{name: "reject other scheme", endpoint: "ftp://example.com", wantErr: "http://"},
		{name: "reject empty", endpoint: "   ", wantErr: "模型 API 地址"},
		{name: "reject missing host", endpoint: "https:///v1", wantErr: "主机名"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := CompletionURL(test.endpoint)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("CompletionURL(%q) error = %v, want %q", test.endpoint, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompletionURL(%q) error = %v", test.endpoint, err)
			}
			if got != test.want {
				t.Fatalf("CompletionURL(%q) = %q, want %q", test.endpoint, got, test.want)
			}
		})
	}
}

func TestDisplayEndpointDropsQuery(t *testing.T) {
	t.Parallel()
	got := DisplayEndpoint("https://gw.example.com/v1/chat/completions?key=secret")
	if strings.Contains(got, "secret") {
		t.Fatalf("DisplayEndpoint kept the query string: %q", got)
	}
}

func TestResolveClampsAndDefaults(t *testing.T) {
	t.Parallel()
	settings := newFakeSettings()
	settings.set(config.AssistantEnabled, true)
	settings.set(config.AssistantEndpoint, "  https://api.deepseek.com/v1  ")
	settings.set(config.AssistantModel, " deepseek-chat ")
	settings.set(config.AssistantSummaryInterval, 2)
	settings.set(config.AssistantContextSentences, 5000)
	settings.set(config.AssistantTimeout, 0)

	cfg := Resolve(settings)
	if !cfg.Enabled || cfg.Endpoint != "https://api.deepseek.com/v1" || cfg.Model != "deepseek-chat" {
		t.Fatalf("resolved config = %#v", cfg)
	}
	if cfg.SummaryInterval != MinSummaryInterval {
		t.Fatalf("summary interval = %v, want clamp to %v", cfg.SummaryInterval, MinSummaryInterval)
	}
	if cfg.ContextSentences != MaxContext {
		t.Fatalf("context sentences = %d, want clamp to %d", cfg.ContextSentences, MaxContext)
	}
	if cfg.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want the 30s default", cfg.Timeout)
	}
}

func TestValidateRequiresEndpointAndModel(t *testing.T) {
	t.Parallel()
	if err := (Config{Model: "gpt-4o-mini"}).Validate(); err == nil {
		t.Fatal("missing endpoint was accepted")
	}
	if err := (Config{Endpoint: "https://api.openai.com/v1"}).Validate(); err == nil {
		t.Fatal("missing model was accepted")
	}
	// A local server without authentication must stay valid.
	if err := (Config{Endpoint: "http://localhost:11434/v1", Model: "qwen3"}).Validate(); err != nil {
		t.Fatalf("local endpoint without key rejected: %v", err)
	}
}
