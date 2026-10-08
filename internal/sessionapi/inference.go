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

// A request changes model, thinking, or both at the next agent turn.
type InferenceUpdateRequest struct {
	ApplyAt          string          `json:"apply_at,omitempty"`
	RequestID        string          `json:"request_id"`
	ExpectedRevision int             `json:"expected_revision"`
	Model            *string         `json:"model,omitempty"`
	Thinking         *string         `json:"thinking,omitempty"`
	ModelDefinition  json.RawMessage `json:"model_definition,omitempty"`
}

type InferenceUpdate struct {
	InferenceUpdateRequest
	Revision    int                `json:"revision"`
	Status      string             `json:"status"`
	Error       string             `json:"error,omitempty"`
	UpdatedAt   string             `json:"updated_at"`
	AttemptID   string             `json:"attempt_id,omitempty"`
	ReceiptPath string             `json:"receipt_path,omitempty"`
	Settings    *InferenceSettings `json:"settings,omitempty"`
}

type InferenceResponse struct {
	ApplyAt  string            `json:"apply_at"`
	Settings InferenceSettings `json:"settings"`
	Revision int               `json:"revision"`
	Update   *InferenceUpdate  `json:"update,omitempty"`
}

var inferenceRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (r InferenceUpdateRequest) Validate() error {
	if r.ApplyAt != "" && r.ApplyAt != "next_turn" {
		return fmt.Errorf("%w: apply_at must be next_turn", ErrInvalidSession)
	}
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
	r := &InferenceResponse{ApplyAt: "next_turn", Settings: InferenceSettings{Model: m.Config.Model, Thinking: m.Config.Thinking}, Update: m.InferenceUpdate}
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

// ClaimInferenceUpdate reserves a pending change for one Pi startup attempt.
func ClaimInferenceUpdate(path, attemptID, receiptPath string, defaults InferenceSettings) (*InferenceUpdate, error) {
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
			u.AttemptID, u.ReceiptPath = attemptID, receiptPath
			settings := InferenceSettings{Model: m.Config.Model, Thinking: m.Config.Thinking}
			if settings.Model == "" {
				settings.Model = defaults.Model
			}
			if settings.Thinking == "" {
				settings.Thinking = defaults.Thinking
			}
			if u.Model != nil {
				settings.Model = *u.Model
			}
			if u.Thinking != nil {
				settings.Thinking = *u.Thinking
			}
			u.Settings = &settings
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
		if status == "applied" && settings != nil {
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
			u.Error = "previous turn ended without confirming startup; saved settings were retained"
			u.UpdatedAt = inferenceTimestamp()
		}
		return nil
	})
	return err
}

func SettleInferenceUpdate(path, detail string) error {
	_, err := MutateManifest(path, func(m *Manifest) error {
		SettleInferenceUpdateInManifest(m, detail)
		return nil
	})
	return err
}

// SettleInferenceUpdateInManifest is called while holding the manifest lock.
func SettleInferenceUpdateInManifest(m *Manifest, detail string) {
	if u := m.InferenceUpdate; u != nil && (u.Status == "pending" || u.Status == "applying") {
		if u.Status == "pending" {
			u.Status = "rejected"
		} else {
			u.Status = "unknown"
		}
		u.Error, u.UpdatedAt = detail, inferenceTimestamp()
	}
}
