package sessionapi

import (
	"encoding/json"
	"testing"
)

func TestSessionRuntimeNormalizesLegacyCloudValue(t *testing.T) {
	var session Session
	if err := json.Unmarshal([]byte(`{"session_id":"sess_1","status":"running","runtime":"hosted"}`), &session); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if session.Runtime != RuntimeCloud {
		t.Fatalf("runtime: got %q", session.Runtime)
	}
}
