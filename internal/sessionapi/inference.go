package sessionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type InferenceSettings struct {
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

// A request changes model, thinking, or both. Combined changes select the pair
// atomically; historical partial outcomes remain readable for compatibility.
type InferenceUpdateRequest struct {
	RequestID        string          `json:"request_id"`
	ExpectedRevision int             `json:"expected_revision"`
	Model            *string         `json:"model,omitempty"`
	Thinking         *string         `json:"thinking,omitempty"`
	ModelDefinition  json.RawMessage `json:"model_definition,omitempty"`
}

type InferenceUpdate struct {
	InferenceUpdateRequest
	Revision  int    `json:"revision"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

type InferenceResponse struct {
	Settings InferenceSettings `json:"settings"`
	Revision int               `json:"revision"`
	Update   *InferenceUpdate  `json:"update,omitempty"`
}

var inferenceRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (r InferenceUpdateRequest) Validate() error {
	if !inferenceRequestID.MatchString(r.RequestID) || r.ExpectedRevision < 0 {
		return fmt.Errorf("%w: valid request_id and non-negative expected_revision are required", ErrInvalidSession)
	}
	if r.Model == nil && r.Thinking == nil {
		return fmt.Errorf("%w: provide model and/or thinking", ErrInvalidSession)
	}
	if r.Model != nil {
		provider, model, ok := strings.Cut(*r.Model, "/")
		if !ok || provider == "" || model == "" || len(*r.Model) > 512 || strings.ContainsAny(*r.Model, " \t\r\n\x00") {
			return fmt.Errorf("%w: model must use <provider>/<model-id>", ErrInvalidSession)
		}
	}
	if r.Thinking != nil && (*r.Thinking == "" || len(*r.Thinking) > 32 || strings.ContainsAny(*r.Thinking, " \t\r\n\x00")) {
		return fmt.Errorf("%w: thinking must be a non-empty level", ErrInvalidSession)
	}
	if len(r.ModelDefinition) > 0 {
		if r.Model == nil || len(r.ModelDefinition) > 64<<10 {
			return fmt.Errorf("%w: model_definition requires a model and must be at most 64 KiB", ErrInvalidSession)
		}
		var definition map[string]json.RawMessage
		if err := json.Unmarshal(r.ModelDefinition, &definition); err != nil || definition == nil {
			return fmt.Errorf("%w: invalid model_definition", ErrInvalidSession)
		}
		allowed := map[string]bool{"id": true, "name": true, "api": true, "reasoning": true, "thinkingLevelMap": true, "input": true, "cost": true, "contextWindow": true, "maxTokens": true, "samplingParams": true, "compat": true}
		for key := range definition {
			if !allowed[key] {
				return fmt.Errorf("%w: model_definition may not override %s", ErrInvalidSession, key)
			}
		}
		var id string
		_, wanted, _ := strings.Cut(*r.Model, "/")
		if json.Unmarshal(definition["id"], &id) != nil || id != wanted {
			return fmt.Errorf("%w: model_definition id must match model", ErrInvalidSession)
		}
	}
	return nil
}

func inferenceResponse(m *Manifest) *InferenceResponse {
	r := &InferenceResponse{Settings: InferenceSettings{Model: m.Config.Model, Thinking: m.Config.Thinking}, Update: m.InferenceUpdate}
	if r.Update != nil {
		r.Revision = r.Update.Revision
	}
	return r
}

func (fs *FileStore) Inference(id string) (*InferenceResponse, error) {
	if !safeSessionID(id) {
		return nil, ErrNotFound
	}
	m, err := ReadManifest(fs.manifestPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return inferenceResponse(m), nil
}

func (fs *FileStore) UpdateInference(id string, req InferenceUpdateRequest) (*InferenceResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if !safeSessionID(id) {
		return nil, ErrNotFound
	}
	m, err := MutateManifest(fs.manifestPath(id), func(m *Manifest) error {
		current := inferenceResponse(m)
		if current.Update != nil && current.Update.RequestID == req.RequestID {
			if current.Update.ExpectedRevision != req.ExpectedRevision || !sameOptionalString(current.Update.Model, req.Model) || !sameOptionalString(current.Update.Thinking, req.Thinking) || !sameJSON(current.Update.ModelDefinition, req.ModelDefinition) {
				return fmt.Errorf("%w: request_id already used with different settings", ErrConflict)
			}
			return nil
		}
		if current.Revision != req.ExpectedRevision {
			return fmt.Errorf("%w: inference settings changed; reload before submitting another change", ErrConflict)
		}
		if m.IsStopped() || (m.SessionKind == KindTask && deriveStatus(fs.sessionDir(id), m).IsTerminal()) {
			return fmt.Errorf("%w: session is not running", ErrConflict)
		}
		if epoch := m.LastEpoch(); m.SessionKind == KindTask && epoch != nil && epoch.FinishedAt != nil {
			return fmt.Errorf("%w: task has already finished", ErrConflict)
		}
		if current.Update != nil && (current.Update.Status == "pending" || current.Update.Status == "applying") {
			return fmt.Errorf("%w: an inference change is already in progress", ErrConflict)
		}
		m.InferenceUpdate = &InferenceUpdate{InferenceUpdateRequest: req, Revision: current.Revision + 1, Status: "pending", UpdatedAt: inferenceTimestamp()}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if m.InferenceUpdate != nil && m.InferenceUpdate.Status == "pending" && fs.OnInferenceUpdate != nil {
		if err := fs.OnInferenceUpdate(id); err != nil {
			return nil, fmt.Errorf("inference change saved but worker notification failed; retry the same request: %w", err)
		}
	}
	return inferenceResponse(m), nil
}

func safeSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00")
}

func sameOptionalString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	_ = json.Unmarshal(a, &av)
	_ = json.Unmarshal(b, &bv)
	return reflect.DeepEqual(av, bv)
}

func inferenceTimestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ClaimInferenceUpdate records intent before sending RPC. An interrupted claim
// is not automatically resent: a command may have applied before the crash.
func ClaimInferenceUpdate(path string) (*InferenceUpdate, error) {
	var claimed *InferenceUpdate
	_, err := MutateManifest(path, func(m *Manifest) error {
		u := m.InferenceUpdate
		if u == nil || u.Status != "pending" {
			return nil
		}
		if m.IsStopped() {
			u.Status, u.Error = "rejected", "session stopped before the change was applied"
		} else {
			u.Status = "applying"
			copy := *u
			claimed = &copy
		}
		u.UpdatedAt = inferenceTimestamp()
		return nil
	})
	return claimed, err
}

func FinishInferenceUpdate(path, requestID, status, detail string, settings *InferenceSettings) error {
	_, err := MutateManifest(path, func(m *Manifest) error {
		u := m.InferenceUpdate
		if u == nil || u.RequestID != requestID || u.Status != "applying" {
			return fmt.Errorf("%w: inference request is no longer active", ErrConflict)
		}
		if (status == "applied" || status == "partial") && settings != nil {
			previousModel := m.Config.Model
			m.Config.Model, m.Config.Thinking = settings.Model, settings.Thinking
			if u.Model != nil && (len(u.ModelDefinition) > 0 || previousModel != settings.Model) {
				m.InferenceModelDefinition = u.ModelDefinition
			}
		}
		u.Status, u.Error, u.UpdatedAt = status, detail, inferenceTimestamp()
		return nil
	})
	return err
}

func RecoverInferenceUpdate(path string) error {
	_, err := MutateManifest(path, func(m *Manifest) error {
		if u := m.InferenceUpdate; u != nil && u.Status == "applying" {
			u.Status = "unknown"
			u.Error = "previous Pi invocation ended without confirming the change; saved settings were retained"
			u.UpdatedAt = inferenceTimestamp()
		}
		return nil
	})
	return err
}

func SettleInferenceUpdate(path, detail string) error {
	_, err := MutateManifest(path, func(m *Manifest) error {
		settleInferenceUpdate(m, detail)
		return nil
	})
	return err
}

func settleInferenceUpdate(m *Manifest, detail string) {
	if u := m.InferenceUpdate; u != nil && (u.Status == "pending" || u.Status == "applying") {
		if u.Status == "pending" {
			u.Status = "rejected"
		} else {
			u.Status = "unknown"
		}
		u.Error, u.UpdatedAt = detail, inferenceTimestamp()
	}
}
