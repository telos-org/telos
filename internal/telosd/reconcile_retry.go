package telosd

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var errPermanentReconcile = errors.New("permanent reconcile failure")

func permanentReconcileError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errPermanentReconcile, fmt.Sprintf(format, args...))
}

func isPermanentReconcileError(err error) bool {
	return errors.Is(err, errPermanentReconcile)
}

type reconcileRetryTracker struct {
	mu      sync.Mutex
	items   map[string]reconcileRetryState
	now     func() time.Time
	backoff func(int) time.Duration
	jitter  func(time.Duration) time.Duration
}

type reconcileRetryState struct {
	Attempts  int
	NextRetry time.Time
	LastError string
	Permanent bool
}

func newReconcileRetryTracker() *reconcileRetryTracker {
	return &reconcileRetryTracker{
		items:   map[string]reconcileRetryState{},
		now:     time.Now,
		backoff: defaultReconcileBackoff,
		jitter:  jitterDuration,
	}
}

func (t *reconcileRetryTracker) shouldRun(key string) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.items[key]
	if !ok {
		return true
	}
	if state.Permanent {
		return false
	}
	return !t.clock().Before(state.NextRetry)
}

func (t *reconcileRetryTracker) recordSuccess(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.items, key)
	t.mu.Unlock()
}

func (t *reconcileRetryTracker) recordError(key string, err error) error {
	if t == nil || err == nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.items == nil {
		t.items = map[string]reconcileRetryState{}
	}
	state := t.items[key]
	state.LastError = err.Error()
	if isPermanentReconcileError(err) {
		state.Permanent = true
		t.items[key] = state
		return err
	}
	state.Attempts++
	delay := t.backoffDelay(state.Attempts)
	state.NextRetry = t.clock().Add(delay)
	t.items[key] = state
	return fmt.Errorf("%w; retry after %s", err, delay)
}

func (t *reconcileRetryTracker) snapshot(key string) (reconcileRetryState, bool) {
	if t == nil {
		return reconcileRetryState{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.items[key]
	return state, ok
}

func (t *reconcileRetryTracker) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *reconcileRetryTracker) backoffDelay(attempt int) time.Duration {
	var delay time.Duration
	if t.backoff != nil {
		delay = t.backoff(attempt)
	} else {
		delay = defaultReconcileBackoff(attempt)
	}
	if t.jitter != nil {
		delay = t.jitter(delay)
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func defaultReconcileBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
}

func jitterDuration(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return base
	}
	value := binary.BigEndian.Uint64(raw[:])
	max := uint64(base / 2)
	if max == 0 {
		return base
	}
	return base + time.Duration(value%max)
}
