package cloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPITokenID(t *testing.T) {
	tests := []struct {
		token    string
		expected string
	}{
		{"telos_pat_tok_abc123.supersecret", "tok_abc123"},
		{"telos_pat_tok_abc123.", "tok_abc123"},
		{"telos_pat_abc123.supersecret", ""},
		{"telos_pat_tok_abc123-no-secret", ""},
		{"legacy-shared-secret", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := APITokenID(tt.token); got != tt.expected {
			t.Errorf("APITokenID(%q) = %q, want %q", tt.token, got, tt.expected)
		}
	}
}

func TestStartAndPollCLIAuth(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("pre-auth endpoint got Authorization header %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/cli/auth/start":
			var body struct {
				ClientName string `json:"client_name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ClientName != "test-host" {
				t.Errorf("client_name = %q, want test-host", body.ClientName)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"request_id":       "cli_req_1",
				"user_code":        "WDJB-MJHT",
				"poll_secret":      "psecret",
				"verification_url": "https://usetelos.ai/cli-auth?code=WDJB-MJHT",
				"expires_in":       600,
				"interval":         5,
			})
		case "/api/cli/auth/poll":
			var body struct {
				RequestID  string `json:"request_id"`
				PollSecret string `json:"poll_secret"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.RequestID != "cli_req_1" || body.PollSecret != "psecret" {
				t.Errorf("poll body = %+v", body)
			}
			polls++
			if polls == 1 {
				json.NewEncoder(w).Encode(map[string]any{"status": "pending", "token": nil, "interval": 5})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"status":   "approved",
				"token":    "telos_pat_tok_new.secret",
				"interval": 5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	start, err := StartCLIAuth(srv.URL, "test-host")
	if err != nil {
		t.Fatal(err)
	}
	if start.UserCode != "WDJB-MJHT" || start.PollSecret != "psecret" {
		t.Errorf("start = %+v", start)
	}

	first, err := PollCLIAuth(srv.URL, start.RequestID, start.PollSecret)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "pending" || first.Token != "" {
		t.Errorf("first poll = %+v", first)
	}

	second, err := PollCLIAuth(srv.URL, start.RequestID, start.PollSecret)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "approved" || second.Token != "telos_pat_tok_new.secret" {
		t.Errorf("second poll = %+v", second)
	}
}

func TestStartCLIAuthSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := StartCLIAuth(srv.URL, "test-host")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsStatus(err, http.StatusNotFound) {
		t.Errorf("IsStatus(err, 404) = false, err = %v", err)
	}
}
