// Package assistant turns finalized captions into rolling key points and
// answers by calling an OpenAI-compatible chat completions endpoint that the
// user supplies. Recognition itself stays local: nothing is sent anywhere until
// the user enables the assistant and configures an endpoint.
package assistant

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/kangzyz/KSpeech/internal/config"
)

const (
	// Bounds keep a hand-edited config.json from turning into a request storm
	// or a request that never times out.
	MinSummaryInterval = 15 * time.Second
	MaxSummaryInterval = 30 * time.Minute
	MinTimeout         = 5 * time.Second
	MaxTimeout         = 3 * time.Minute
	MinContext         = 4
	MaxContext         = 200

	defaultSummaryInterval = 90 * time.Second
	defaultTimeout         = 30 * time.Second
	defaultContext         = 30
)

// Settings is the read side of the configuration store.
type Settings interface {
	String(key string) string
	Bool(key string) bool
	Int(key string) int
}

// Config is one resolved snapshot of the user's endpoint settings. Every
// request resolves it again so edits apply without restarting a recognition
// run.
type Config struct {
	Enabled          bool
	Endpoint         string
	APIKey           string
	Model            string
	Summarize        bool
	AutoAnswer       bool
	Tools            bool
	SummaryInterval  time.Duration
	ContextSentences int
	Timeout          time.Duration
	Background       string
}

func Resolve(settings Settings) Config {
	return Config{
		Enabled:          settings.Bool(config.AssistantEnabled),
		Endpoint:         strings.TrimSpace(settings.String(config.AssistantEndpoint)),
		APIKey:           strings.TrimSpace(settings.String(config.AssistantAPIKey)),
		Model:            strings.TrimSpace(settings.String(config.AssistantModel)),
		Summarize:        settings.Bool(config.AssistantSummarize),
		AutoAnswer:       settings.Bool(config.AssistantAutoAnswer),
		Tools:            settings.Bool(config.AssistantTools),
		SummaryInterval:  clampDuration(settings.Int(config.AssistantSummaryInterval), MinSummaryInterval, MaxSummaryInterval, defaultSummaryInterval),
		ContextSentences: clampInt(settings.Int(config.AssistantContextSentences), MinContext, MaxContext, defaultContext),
		Timeout:          clampDuration(settings.Int(config.AssistantTimeout), MinTimeout, MaxTimeout, defaultTimeout),
		Background:       strings.TrimSpace(settings.String(config.AssistantBackground)),
	}
}

// Validate reports whether the endpoint settings are complete enough to send a
// request. An empty API key is allowed because local servers such as Ollama or
// LM Studio do not use one.
func (c Config) Validate() error {
	if _, err := CompletionURL(c.Endpoint); err != nil {
		return err
	}
	if c.Model == "" {
		return errors.New("请先填写模型名称，例如 gpt-4o-mini、deepseek-chat 或 qwen-plus")
	}
	return nil
}

// CompletionURL turns the configured base address into the chat completions
// URL. A base that already points at the endpoint is used as is, so both
// "https://api.deepseek.com/v1" and a full ".../chat/completions" work.
func CompletionURL(endpoint string) (string, error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return "", errors.New("请先填写模型 API 地址，例如 https://api.deepseek.com/v1")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("模型 API 地址无法解析：%w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		// An API key travels in a header, so plain HTTP would put it on the
		// wire in the clear. Local and intranet servers are the legitimate
		// case and stay allowed.
		if !isLocalHost(parsed.Hostname()) {
			return "", errors.New("http 地址只允许本机或内网服务，公网请改用 https，否则 API Key 会以明文发送")
		}
	default:
		return "", errors.New("模型 API 地址必须以 http:// 或 https:// 开头")
	}
	if parsed.Host == "" {
		return "", errors.New("模型 API 地址缺少主机名")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") {
		path += "/chat/completions"
	}
	parsed.Path = path
	parsed.Fragment = ""
	return parsed.String(), nil
}

// DisplayEndpoint drops the query string so an endpoint can be quoted in an
// error message without leaking a key that a gateway expects as a parameter.
func DisplayEndpoint(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func isLocalHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if address := net.ParseIP(host); address != nil {
		return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast()
	}
	// A single-label name can only resolve inside the local network.
	return host != "" && !strings.Contains(host, ".")
}

func clampInt(value, minimum, maximum, fallback int) int {
	if value <= 0 {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func clampDuration(seconds int, minimum, maximum, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	value := time.Duration(seconds) * time.Second
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
