package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"syscall"
	"time"
)

const (
	readMaxAttempts = 3
	readRetryDelay  = 250 * time.Millisecond
)

// getJSONWithRetry is opt-in for monitoring GETs. Keep writes and token claims
// on the single-attempt path: a lost response does not mean they were rejected.
func getJSONWithRetry[T any](ctx context.Context, c *Client, path string) (*T, error) {
	timeout := c.HTTP.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := getJSONAttempt[T](ctx, c, path)
		if err == nil || attempt+1 == readMaxAttempts || !retryableReadError(err) {
			return result, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Equal jitter spreads clients across the latter half of each backoff.
		delay := readRetryDelay << attempt
		delay = delay/2 + time.Duration(rand.Int64N(int64(delay/2)))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func getJSONAttempt[T any](ctx context.Context, c *Client, path string) (*T, error) {
	resp, err := c.doRawContext(ctx, http.MethodGet, path, nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	// Complete the transfer so truncated bodies retry, but malformed JSON does not.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func retryableReadError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	// SERVFAIL can be temporary without being a timeout.
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsTemporary {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
