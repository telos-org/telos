package executor

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/option"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/gatewaycred"
	"github.com/telos-org/telos/internal/sessionapi"
)

// bifrostRouting implements the managed gateway's sticky-routing contract for
// the native executor: every request carries Bifrost session headers, and the
// provider Bifrost assigns on the first successful agent response is persisted
// to the session manifest so later turns (and resumed sessions) pin to it.
//
// The routing snapshot (phase, assigned provider) is loaded once per turn, like
// the pi extension it replaces, which froze the values into env for the whole
// turn. Compaction requests advertise their own session/cache keys so Bifrost
// never routes a summary through the agent's provider cache.
type bifrostRouting struct {
	sessionID  string
	sessionDir string
	profile    gatewaycred.ModelProfile
	requestID  string

	assignedProvider string
	phase            string

	mu sync.Mutex
}

const bifrostModelPrefix = "telos-bifrost/"

// bifrostRoutingActive reports whether the credential/model combination is
// served by a Bifrost gateway and needs sticky-routing headers.
func bifrostRoutingActive(kind gatewayKind, model string) bool {
	return kind == gatewayKindBifrost || strings.HasPrefix(strings.TrimSpace(model), bifrostModelPrefix)
}

// newBifrostRouting loads the turn's routing snapshot from the session
// manifest. A missing manifest or a profile change resets the assignment.
func newBifrostRouting(sessionID, sessionDir string, profile gatewaycred.ModelProfile, turnState *game.TurnState, role string) *bifrostRouting {
	profile, err := gatewaycred.NormalizeModelProfile(string(profile))
	if err != nil {
		profile = gatewaycred.ModelProfileStandard
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "unknown"
	}
	requestID := sessionID
	if turnState != nil {
		requestID = fmt.Sprintf("%s:%d:%d:%s", sessionID, turnState.EpochID, turnState.RoundNum, role)
	}
	r := &bifrostRouting{
		sessionID:  sessionID,
		sessionDir: strings.TrimSpace(sessionDir),
		profile:    profile,
		requestID:  requestID,
		phase:      "new",
	}
	if state := r.readState(); state != nil && strings.TrimSpace(state.AssignedProvider) != "" {
		r.assignedProvider = strings.TrimSpace(state.AssignedProvider)
		r.phase = "existing"
	}
	return r
}

func (r *bifrostRouting) readState() *sessionapi.GatewayRoutingState {
	if r.sessionDir == "" {
		return nil
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(r.sessionDir, "session.json"))
	if err != nil || manifest.GatewayRouting == nil {
		return nil
	}
	stateProfile, err := sessionapi.NormalizeModelProfile(string(manifest.GatewayRouting.ModelProfile))
	if err != nil || stateProfile != r.profile {
		return nil
	}
	return manifest.GatewayRouting
}

// requestModel strips the telos-bifrost namespace: the gateway's model catalog
// uses the bare profile-model names.
func bifrostRequestModel(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), bifrostModelPrefix)
}

// compactionModel is the profile's dedicated compaction model, sent bare.
func (r *bifrostRouting) compactionModel() string {
	return bifrostRequestModel(sessionapi.BifrostCompactionModel(r.profile))
}

func (r *bifrostRouting) assignedOrUnset() string {
	if r.assignedProvider != "" {
		return r.assignedProvider
	}
	return "unset"
}

// agentHeaders is the sticky-routing header set for agent-loop requests.
func (r *bifrostRouting) agentHeaders() map[string]string {
	assigned := r.assignedOrUnset()
	phase := r.phase
	if assigned == "unset" {
		phase = "new"
	}
	return map[string]string{
		"x-bf-session-id":         r.sessionID,
		"x-bf-session-ttl":        "1h",
		"x-bf-cache-key":          r.sessionID,
		"x-llm-usecase":           "agent",
		"x-llm-session-phase":     phase,
		"x-llm-assigned-provider": assigned,
		"x-telos-model-profile":   string(r.profile),
		"x-request-id":            r.requestID,
	}
}

// compactionHeaders is the header set for compaction requests. Standard-profile
// compaction always routes to the fixed compaction provider; premium follows
// the agent assignment.
func (r *bifrostRouting) compactionHeaders() map[string]string {
	assigned := r.assignedOrUnset()
	if r.profile == gatewaycred.ModelProfileStandard {
		assigned = "silares"
	}
	return map[string]string{
		"x-bf-session-id":         r.sessionID + ":compaction",
		"x-bf-session-ttl":        "1h",
		"x-bf-cache-key":          r.sessionID + ":compaction",
		"x-llm-usecase":           "compaction",
		"x-llm-session-phase":     "existing",
		"x-llm-assigned-provider": assigned,
		"x-telos-model-profile":   string(r.profile),
		"x-request-id":            r.requestID + ":compaction",
	}
}

// agentOptions returns the per-request options for agent-loop requests.
func (r *bifrostRouting) agentOptions() []option.RequestOption {
	if r == nil {
		return nil
	}
	return r.options(r.agentHeaders(), sessionapi.BifrostAgentModel(r.profile))
}

// compactionOptions returns the per-request options for compaction requests.
func (r *bifrostRouting) compactionOptions() []option.RequestOption {
	if r == nil {
		return nil
	}
	return r.options(r.compactionHeaders(), sessionapi.BifrostCompactionModel(r.profile))
}

func (r *bifrostRouting) options(headers map[string]string, fallbackModel string) []option.RequestOption {
	opts := make([]option.RequestOption, 0, len(headers)+1)
	for _, key := range sortedMapKeys(headers) {
		opts = append(opts, option.WithHeader(key, headers[key]))
	}
	opts = append(opts, option.WithMiddleware(r.captureMiddleware(fallbackModel)))
	return opts
}

// captureMiddleware observes the gateway's routing response headers and
// persists the assignment. It never fails the request.
func (r *bifrostRouting) captureMiddleware(fallbackModel string) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		resp, err := next(req)
		if resp != nil {
			r.observe(resp, fallbackModel)
		}
		return resp, err
	}
}

func (r *bifrostRouting) observe(resp *http.Response, fallbackModel string) {
	provider := firstHeader(resp.Header, "x-bifrost-provider", "x-telos-routed-provider")
	routedModel := firstHeader(resp.Header, "x-bifrost-original-model", "x-telos-routed-model")
	if routedModel == "" {
		routedModel = fallbackModel
	}
	if provider == "" && routedModel == "" {
		return
	}
	fallbackIndex, _ := strconv.Atoi(firstHeader(resp.Header, "x-bifrost-fallback-index"))
	fallback := fallbackIndex > 0 ||
		strings.EqualFold(firstHeader(resp.Header, "x-telos-routing-fallback"), "true")
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	_ = r.updateState(bifrostRoutingObservation{
		Provider: provider,
		Model:    routedModel,
		Fallback: fallback,
		OK:       ok,
	})
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

type bifrostRoutingObservation struct {
	Provider string
	Model    string
	Fallback bool
	OK       bool
}

// updateState persists the observation into the session manifest. The provider
// assignment is written once, from the first successful agent-model response.
func (r *bifrostRouting) updateState(route bifrostRoutingObservation) error {
	if r.sessionDir == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	manifestPath := filepath.Join(r.sessionDir, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	state := manifest.GatewayRouting
	if state != nil {
		stateProfile, err := sessionapi.NormalizeModelProfile(string(state.ModelProfile))
		if err != nil || stateProfile != r.profile {
			state = nil
		}
	}
	if state == nil {
		state = &sessionapi.GatewayRoutingState{ModelProfile: r.profile}
		manifest.GatewayRouting = state
	}
	state.ModelProfile = r.profile
	if route.Model != "" {
		state.LastModel = route.Model
	}
	state.LastFallback = route.Fallback
	state.LastSeenAt = now
	if route.OK && route.Provider != "" && isBifrostAgentModel(route.Model) && state.AssignedProvider == "" {
		state.AssignedProvider = route.Provider
		state.AssignedAt = now
	}
	return sessionapi.WriteManifest(manifestPath, manifest)
}

func isBifrostAgentModel(model string) bool {
	return strings.HasSuffix(bifrostRequestModel(model), "-agent")
}
