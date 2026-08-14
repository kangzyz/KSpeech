package resource

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func validateResourceURL(rawURL string, allowInsecureHTTP bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid absolute resource URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return parsed, nil
	case "http":
		if allowInsecureHTTP {
			return parsed, nil
		}
		return nil, fmt.Errorf("%w: plaintext HTTP URL %q is disabled", ErrInsecureTransport, rawURL)
	default:
		return nil, fmt.Errorf("invalid resource URL scheme %q", parsed.Scheme)
	}
}

// doRequest clones the configured client so redirect validation can be added
// without mutating a caller-owned http.Client. HTTPS-only policy is checked on
// every hop, including redirects supplied by a custom transport.
func (m *Manager) doRequest(request *http.Request) (*http.Response, error) {
	if _, err := validateResourceURL(request.URL.String(), m.allowInsecureHTTP); err != nil {
		return nil, err
	}
	client := *m.client
	configuredRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if _, err := validateResourceURL(next.URL.String(), m.allowInsecureHTTP); err != nil {
			return err
		}
		if configuredRedirect != nil {
			return configuredRedirect(next, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.Request == nil || response.Request.URL == nil {
		response.Body.Close()
		return nil, errors.New("resource response has no final URL")
	}
	if _, err := validateResourceURL(response.Request.URL.String(), m.allowInsecureHTTP); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}
