package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const httpMaxAttempts = 3

// httpRetryBackoff is the base delay between transient HTTP retries. It is a
// var so tests can shrink it.
var httpRetryBackoff = 500 * time.Millisecond

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type httpPoster struct {
	http *http.Client
	cfg  nativeProviderConfig
}

func (p httpPoster) client() *http.Client {
	if p.http != nil {
		return p.http
	}
	return http.DefaultClient
}

func (p httpPoster) postJSON(ctx context.Context, endpoint string, body, out interface{}) error {
	return p.post(ctx, endpoint, body, out, map[string]string{"Authorization": "Bearer " + p.cfg.APIKey})
}

func (p httpPoster) postAnthropic(ctx context.Context, endpoint string, body, out interface{}) error {
	return p.post(ctx, endpoint, body, out, map[string]string{
		"x-api-key":         p.cfg.APIKey,
		"anthropic-version": "2023-06-01",
	})
}

func (p httpPoster) post(ctx context.Context, endpoint string, body, out interface{}, headers map[string]string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("provider_request_encode:%w", err)
	}
	url := p.cfg.BaseURL + endpoint
	var lastErr error
	for attempt := 0; attempt < httpMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(httpRetryBackoff * time.Duration(1<<(attempt-1))):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("provider_request_create:%w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := p.client().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("provider_request_failed:%w", err)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("provider_response_read:%w", readErr)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("provider_response_decode:%w", err)
			}
			return nil
		}
		lastErr = fmt.Errorf("provider_http_%d:%s", resp.StatusCode, strings.TrimSpace(string(data)))
		if !isRetryableStatus(resp.StatusCode) {
			return lastErr
		}
	}
	return lastErr
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
