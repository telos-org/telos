package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRevisionClientPreservesContextAndActionPrecondition(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer revision-token" || r.Header.Get("X-Telos-Org-Id") != "org_team" {
			t.Errorf("missing revision request authentication or context")
		}
		switch r.URL.Path {
		case "/api/deployments/sess_test/revisions":
			if r.URL.RawQuery != "before=9&limit=3" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			io.WriteString(w, `{"current_revision_id":"rev_12","revisions":[{"id":"rev_8","sequence":8}],"next_before":8}`)
		case "/api/deployments/sess_test/revisions/rev_7/restore":
			posts.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body) != 2 || body["expected_current_revision_id"] != "rev_12" || body["revision_message"] != "Back to notes" {
				t.Errorf("body = %#v", body)
			}
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":{"code":"stale_current_revision","message":"Current revision changed"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "revision-token")
	client.OrgID = "org_team"
	page, err := client.ListSessionRevisions(context.Background(), "sess_test", 3, 9)
	if err != nil || page.Revisions[0].Sequence != 8 {
		t.Fatalf("page = %+v, err = %v", page, err)
	}
	_, err = client.StartRevisionOperation(context.Background(), "sess_test", "rev_7", "restore", RevisionActionOptions{
		ExpectedCurrentRevisionID: "rev_12", RevisionMessage: "Back to notes",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "stale_current_revision" || posts.Load() != 1 {
		t.Fatalf("error = %v, requests = %d", err, posts.Load())
	}
}

func TestRevisionClientRejectsMalformedHistory(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"current_revision_id":"rev_9","revisions":[],"next_before":9}`,
		`{"current_revision_id":"rev_9","revisions":[{"id":"rev_8","sequence":8}],"next_before":10}`,
		`{"current_revision_id":"rev_9","revisions":[{"id":"rev_7","sequence":7},{"id":"rev_8","sequence":8}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			if _, err := NewClient(server.URL, "").ListSessionRevisions(context.Background(), "sess_test", 50, 0); err == nil {
				t.Fatal("accepted malformed history")
			}
		})
	}
}

func TestRevisionClientChecksReadIdentities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/") {
			io.WriteString(w, `{"id":"op_other","status":"succeeded","result_revision_id":"rev_13"}`)
		} else {
			io.WriteString(w, `{"id":"rev_other","sequence":7}`)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "")
	if _, err := client.GetSessionRevision(context.Background(), "sess_test", "rev_7"); err == nil {
		t.Fatal("accepted mismatched revision")
	}
	if _, err := client.GetRevisionOperation(context.Background(), "sess_test", "op_expected"); err == nil {
		t.Fatal("accepted mismatched operation")
	}
}

func TestRevisionClientDoesNotRetryLostMutationResponse(t *testing.T) {
	var attempts int
	client := NewClient("https://api.example.test", "")
	client.HTTP.Transport = readRetryTransport(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, io.ErrUnexpectedEOF
	})
	_, err := client.StartRevisionOperation(context.Background(), "sess_test", "rev_7", "redeploy", RevisionActionOptions{ExpectedCurrentRevisionID: "rev_12"})
	if !errors.Is(err, io.ErrUnexpectedEOF) || attempts != 1 {
		t.Fatalf("attempts = %d, err = %v", attempts, err)
	}
}

func TestRevisionSkillBundleUsesDeploymentAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/deployments/sess_test/revisions/rev_7/skills/team/check/1.0.0/bundle" || r.URL.Query().Get("digest") != "sha256:abc" {
			t.Errorf("URL = %s", r.URL)
		}
		io.WriteString(w, "bundle")
	}))
	defer server.Close()
	data, err := NewClient(server.URL, "").DownloadRevisionSkillBundle(context.Background(), "sess_test", "rev_7", "team", "check", "1.0.0", "sha256:abc")
	if err != nil || string(data) != "bundle" {
		t.Fatalf("data = %s, err = %v", data, err)
	}
}
