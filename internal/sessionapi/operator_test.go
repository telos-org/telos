package sessionapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

// newGoal returns a server that takes the operator token "operator-token",
// its store, and the ID of a goal on it.
func newGoal(t *testing.T) (*httptest.Server, *sessionapi.FileStore, string) {
	t.Helper()
	srv, store := newBearerTestServer(t, "operator-token")
	t.Cleanup(srv.Close)
	created, err := store.UpdateSpec("shop", sessionapi.SessionSpecUpdateRequest{
		SpecMarkdown: "---\nversion: 0.1.0\nname: shop\nplatform: cloud\n---\n# Shop\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, store, created.Session.SessionID
}

// operatorCall sends a request with the operator token and returns the
// status and body of the answer.
func operatorCall(t *testing.T, srv *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer operator-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(answer)
}

func TestPauseAndResumeLeaveTheAgentsANote(t *testing.T) {
	srv, store, id := newGoal(t)
	base := "/api/sessions/" + id
	for _, step := range []struct{ method, path, body string }{
		{"PUT", "/pause", `{"paused":true}`}, // Cloud's status poll: no note
		{"PUT", "/pause", `{"paused":true,"message":"Fixing the header by hand.","operator":"ada@example.com"}`},
		{"POST", "/resume", `{"message":"Fixed the header.","operator":"ada@example.com"}`},
	} {
		if code, body := operatorCall(t, srv, step.method, base+step.path, step.body); code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", step.method, step.path, code, body)
		}
	}
	for _, step := range []struct{ method, path, body string }{
		{"POST", "/resume", `{"message":"No name."}`},
		{"PUT", "/pause", `{"paused":false,"message":"Back to work.","operator":"ada@example.com"}`}, // a note only goes with pausing
	} {
		if code, _ := operatorCall(t, srv, step.method, base+step.path, step.body); code != http.StatusBadRequest {
			t.Fatalf("%s %s %s = %d, want 400", step.method, step.path, step.body, code)
		}
	}

	session, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := os.ReadFile(filepath.Join(filepath.Dir(*session.Specs[0].EvidencePath), "transcript-"+id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "<external_update>\nSource: pause\n\nOperator: ada@example.com\n\nFixing the header by hand.\n</external_update>\n" +
		"\n## External Update\n\n" +
		"<external_update>\nSource: resume\n\nOperator: ada@example.com\n\nFixed the header.\n</external_update>\n"
	if strings.Count(string(transcript), "<external_update>") != 2 || !strings.HasSuffix(string(transcript), want) {
		t.Fatalf("transcript does not end with the two notes:\n%s", transcript)
	}

	events, err := store.Events(id)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, event := range events {
		if event.Event == "external_update" {
			notes = append(notes, event.Data["source"].(string)+": "+event.Data["message"].(string))
		}
	}
	if strings.Join(notes, "|") != "pause: Fixing the header by hand.|resume: Fixed the header." {
		t.Fatalf("evidence notes = %q", notes)
	}
}
