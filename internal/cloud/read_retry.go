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
		if err == nil {
			return result, nil
		}
		var apiError *APIError
		if errors.As(err, &apiError) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt+1 >= readMaxAttempts || !retryableReadError(err) {
			return nil, err
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
	// Finish reading the HTTP body before decoding. A truncated transfer is
	// retryable; malformed JSON in a complete response is a terminal API bug.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Each attempt owns its result, so partial data cannot leak into a later
	// response or the caller's log output.
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
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
