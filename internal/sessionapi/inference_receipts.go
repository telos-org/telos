package sessionapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Settings are the committed defaults when this operation was observed or ended.
// An unknown outcome never promotes unconfirmed startup settings to defaults.
type InferenceOperation struct {
	RequestID string            `json:"request_id"`
	Revision  int               `json:"revision"`
	Status    string            `json:"status"`
	Error     string            `json:"error,omitempty"`
	Settings  InferenceSettings `json:"settings"`
	UpdatedAt string            `json:"updated_at"`
}

type InferenceReceipt struct {
	InferenceOperation
	RequestDigest string `json:"request_digest"`
}

func (fs *FileStore) InferenceRequest(id, requestID string) (*InferenceResponse, error) {
	if !safeSessionID(id) || !inferenceRequestID.MatchString(requestID) {
		return nil, ErrInvalidSession
	}
	m, err := ReadManifest(fs.manifestPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	operation := inferenceOperation(m, requestID)
	if operation == nil {
		return nil, fmt.Errorf("%w: inference request not found", ErrNotFound)
	}
	response := inferenceResponse(m)
	response.Operation = operation
	return response, nil
}

func inferenceOperation(m *Manifest, requestID string) *InferenceOperation {
	if receipt, ok := m.InferenceReceipts[requestID]; ok {
		operation := receipt.InferenceOperation
		return &operation
	}
	update := m.InferenceUpdate
	if update == nil || update.RequestID != requestID {
		return nil
	}
	return &InferenceOperation{
		RequestID: update.RequestID, Revision: update.Revision, Status: update.Status,
		Error: update.Error, UpdatedAt: update.UpdatedAt,
		Settings: InferenceSettings{Model: m.Config.Model, Thinking: m.Config.Thinking},
	}
}

func inferenceRequestDigest(req InferenceUpdateRequest) string {
	// ApplyAt has one semantic value; JSON key order and whitespace are immaterial.
	req.ApplyAt = "next_turn"
	data, _ := json.Marshal(req)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	data, _ = json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func replayInferenceRequest(m *Manifest, req InferenceUpdateRequest) (bool, error) {
	if receipt, ok := m.InferenceReceipts[req.RequestID]; ok {
		if receipt.RequestDigest != inferenceRequestDigest(req) {
			return false, fmt.Errorf("%w: request_id already used with different settings", ErrConflict)
		}
		return true, nil
	}
	if update := m.InferenceUpdate; update != nil && update.RequestID == req.RequestID {
		if !sameInferenceRequest(update.InferenceUpdateRequest, req) {
			return false, fmt.Errorf("%w: request_id already used with different settings", ErrConflict)
		}
		return true, nil
	}
	return false, nil
}

func recordInferenceReceipt(m *Manifest) {
	update := m.InferenceUpdate
	if update == nil || (update.Status != "applied" && update.Status != "rejected" && update.Status != "unknown") {
		return
	}
	if _, exists := m.InferenceReceipts[update.RequestID]; exists {
		return
	}
	operation := inferenceOperation(m, update.RequestID)
	if m.InferenceReceipts == nil {
		m.InferenceReceipts = make(map[string]InferenceReceipt)
	}
	m.InferenceReceipts[update.RequestID] = InferenceReceipt{
		InferenceOperation: *operation, RequestDigest: inferenceRequestDigest(update.InferenceUpdateRequest),
	}
}

func rejectInferenceRequest(m *Manifest, req InferenceUpdateRequest, detail string) {
	if m.InferenceReceipts == nil {
		m.InferenceReceipts = make(map[string]InferenceReceipt)
	}
	m.InferenceReceipts[req.RequestID] = InferenceReceipt{
		InferenceOperation: InferenceOperation{
			RequestID: req.RequestID, Revision: req.ExpectedRevision + 1,
			Status: "rejected", Error: detail, UpdatedAt: inferenceTimestamp(),
			Settings: InferenceSettings{Model: m.Config.Model, Thinking: m.Config.Thinking},
		},
		RequestDigest: inferenceRequestDigest(req),
	}
}
