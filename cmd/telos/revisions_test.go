package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

const revisionTestPath = "/api/deployments/sess_test/revisions"

func testRevision(sequence int) cloud.Revision {
	revision := cloud.Revision{
		ID: fmt.Sprintf("rev_%d", sequence), Sequence: sequence, Kind: "apply",
		PackageRef: "@team/demo:1.0.0", PackageDigest: "sha256:historical",
		Message: fmt.Sprintf("Update %d", sequence), CommittedAt: "2026-10-08T12:00:00Z",
		RestoreCapability: cloud.RevisionCapability{Allowed: true}, RedeployCapability: cloud.RevisionCapability{Allowed: true},
	}
	revision.CreatedBy.Subject = "user_james"
	revision.Snapshot.Status = "available"
	return revision
}

func testRevisionCapabilities() *cloud.Capabilities {
	return &cloud.Capabilities{DeploymentRevisionHistory: true, DeploymentSnapshotRestore: true, DeploymentPackageRedeploy: true, DeploymentRevisionMessages: true}
}

func revisionTestServer(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-token" {
			t.Error("missing authentication")
		}
		if handler, ok := routes[r.Method+" "+r.URL.Path]; ok {
			handler(w, r)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/capabilities":
			_ = json.NewEncoder(w).Encode(testRevisionCapabilities())
		case "GET /api/deployments/sess_test":
			_ = json.NewEncoder(w).Encode(cloud.SessionRecord{ID: "sess_test", Name: "demo"})
		case "GET " + revisionTestPath:
			_ = json.NewEncoder(w).Encode(cloud.RevisionPage{CurrentRevisionID: "rev_12", Revisions: []cloud.Revision{testRevision(12), testRevision(7)}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "")
	return server
}

func TestRevisionCLIProcess(t *testing.T) {
	if os.Getenv("TELOS_TEST_REVISION_COMMAND") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index == -1 {
		os.Exit(2)
	}
	os.Args = append([]string{"telos"}, os.Args[index+1:]...)
	main()
	os.Exit(0)
}

func runRevisionCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestRevisionCLIProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "TELOS_TEST_REVISION_COMMAND=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), exitErr.ExitCode()
}

func TestHistoryCLIListsAndInspectsPaginatedRevisions(t *testing.T) {
	var pageReads atomic.Int32
	revisionTestServer(t, map[string]http.HandlerFunc{
		"GET " + revisionTestPath: func(w http.ResponseWriter, r *http.Request) {
			pageReads.Add(1)
			before, _ := strconv.Atoi(r.URL.Query().Get("before"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			start := 60
			if before > 0 {
				start = before - 1
			}
			page := cloud.RevisionPage{CurrentRevisionID: "rev_60", Revisions: []cloud.Revision{}}
			for sequence := start; sequence > 0 && len(page.Revisions) < limit; sequence-- {
				page.Revisions = append(page.Revisions, testRevision(sequence))
			}
			if len(page.Revisions) > 0 && page.Revisions[len(page.Revisions)-1].Sequence > 1 {
				cursor := page.Revisions[len(page.Revisions)-1].Sequence
				page.NextBefore = &cursor
			}
			_ = json.NewEncoder(w).Encode(page)
		},
		"GET " + revisionTestPath + "/rev_7": func(w http.ResponseWriter, r *http.Request) {
			revision := testRevision(7)
			reason := "snapshot_lost"
			revision.RestoreCapability = cloud.RevisionCapability{Reason: &reason}
			_ = json.NewEncoder(w).Encode(revision)
		},
	})
	stdout, stderr, code := runRevisionCLI(t, "history", "sess_test")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "60*") || !strings.Contains(stdout, "--before 11") || !strings.Contains(stdout, "Update 60") {
		t.Fatalf("history: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	pageReads.Store(0)
	stdout, stderr, code = runRevisionCLI(t, "history", "sess_test", "--revision", "7", "--json")
	var detail struct {
		Revision cloud.Revision `json:"revision"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &detail) != nil || detail.Revision.Sequence != 7 || pageReads.Load() != 2 {
		t.Fatalf("old revision: exit=%d stdout=%s stderr=%s reads=%d", code, stdout, stderr, pageReads.Load())
	}
	stdout, stderr, code = runRevisionCLI(t, "history", "sess_test", "--revision", "rev_7")
	if code != 0 || !strings.Contains(stdout, "saved snapshot is no longer available") || !strings.Contains(stdout, "Redeploy           available") {
		t.Fatalf("detail: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	stdout, stderr, code = runRevisionCLI(t, "history", "sess_test", "--all", "--limit", "20", "--json")
	var all cloud.RevisionPage
	if code != 0 || json.Unmarshal([]byte(stdout), &all) != nil || len(all.Revisions) != 60 || all.NextBefore != nil {
		t.Fatalf("all history: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestHistoryCLIRejectsChangingCurrentRevision(t *testing.T) {
	revisionTestServer(t, map[string]http.HandlerFunc{
		"GET " + revisionTestPath: func(w http.ResponseWriter, r *http.Request) {
			cursor := 12
			page := cloud.RevisionPage{CurrentRevisionID: "rev_12", Revisions: []cloud.Revision{testRevision(12)}, NextBefore: &cursor}
			if r.URL.Query().Get("before") != "" {
				page = cloud.RevisionPage{CurrentRevisionID: "rev_13", Revisions: []cloud.Revision{testRevision(7)}}
			}
			_ = json.NewEncoder(w).Encode(page)
		},
	})
	for _, flags := range [][]string{{"--all"}, {"--revision", "7"}} {
		stdout, stderr, code := runRevisionCLI(t, append([]string{"history", "sess_test"}, flags...)...)
		if code == 0 || stdout != "" || !strings.Contains(stderr, "current revision changed") {
			t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	}
}

func TestRevisionCLIRejectsInvalidInputBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	configureCloudTest(t, server.URL)
	for _, args := range [][]string{
		{"history", "local_test"}, {"history", "sess_test", "--limit", "101"},
		{"history", "sess_test", "--before", "0"}, {"history", "sess_test", "--revision", "0"},
		{"history", "sess_test", "--revision", "7", "--all"}, {"diff", "sess_test", "HEAD"},
		{"restore", "sess_test"}, {"restore", "sess_test", "--revision", "7", "--timeout", "1s"},
		{"redeploy", "sess_test", "--revision", "7", "--wait", "--timeout", "0s"},
		{"restore", "sess_test", "--revision", "7", "--message", "two\nlines"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, code := runRevisionCLI(t, args...)
			if code == 0 || !strings.Contains(stderr, "error:") {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid commands made %d requests", requests.Load())
	}
}

func TestRevisionActionCLIAcceptsOnceAndWaitsForExactResult(t *testing.T) {
	for _, action := range []string{"restore", "redeploy"} {
		for _, wait := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wait=%t", action, wait), func(t *testing.T) {
				var posts, resultReads atomic.Int32
				revisionTestServer(t, map[string]http.HandlerFunc{
					"POST " + revisionTestPath + "/rev_7/" + action: func(w http.ResponseWriter, r *http.Request) {
						posts.Add(1)
						var body cloud.RevisionActionOptions
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ExpectedCurrentRevisionID != "rev_12" || body.RevisionMessage != "Back to stable" {
							t.Errorf("unexpected body: %+v, %v", body, err)
						}
						w.WriteHeader(http.StatusAccepted)
						fmt.Fprintf(w, `{"id":"op_exact","kind":%q,"status":"succeeded","result_revision_id":"rev_13"}`, action)
					},
					"GET " + revisionTestPath + "/rev_13": func(w http.ResponseWriter, r *http.Request) {
						resultReads.Add(1)
						revision := testRevision(13)
						revision.Kind = action
						parent, source := "rev_12", "rev_7"
						revision.ParentRevisionID, revision.RestoredFromRevisionID = &parent, &source
						_ = json.NewEncoder(w).Encode(revision)
					},
				})
				args := []string{action, "sess_test", "--revision", "7", "--message", " Back to stable ", "--json"}
				if wait {
					args = append(args, "--wait")
				}
				stdout, stderr, code := runRevisionCLI(t, args...)
				var receipt revisionActionReceipt
				if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &receipt) != nil || receipt.Operation == nil || receipt.Operation.ID != "op_exact" || posts.Load() != 1 {
					t.Fatalf("exit=%d stdout=%s stderr=%s posts=%d", code, stdout, stderr, posts.Load())
				}
				if wait != (receipt.ResultRevision != nil) || (wait && (receipt.ResultRevision.ID != "rev_13" || resultReads.Load() != 1)) {
					t.Fatalf("incorrect result observation: %+v reads=%d", receipt, resultReads.Load())
				}
			})
		}
	}
}

func TestRevisionActionCLIReportsFailuresWithoutRetry(t *testing.T) {
	for _, tt := range []struct {
		name, response, want string
		status               int
	}{
		{"stale", `{"error":{"code":"stale_current_revision","message":"Current revision changed"}}`, "Current revision changed", 409},
		{"dispatch failure after commit", `{"detail":"Runtime update rejected"}`, "check history before retrying", 409},
		{"server failure", `{"error":{"code":"internal_error","message":"Lost response"}}`, "check history before retrying", 503},
		{"protected", `{"detail":{"code":"change_requests_client_required","message":"Use change requests"}}`, "use the Telos web UI", 409},
		{"failed operation", `{"id":"op_exact","status":"failed","error":{"code":"snapshot_lost","message":"Snapshot disappeared"}}`, "Snapshot disappeared", 202},
		{"timeout", `{"id":"op_exact","status":"running"}`, "the Cloud operation was not cancelled", 202},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var posts atomic.Int32
			revisionTestServer(t, map[string]http.HandlerFunc{
				"POST " + revisionTestPath + "/rev_7/restore": func(w http.ResponseWriter, r *http.Request) {
					posts.Add(1)
					w.WriteHeader(tt.status)
					fmt.Fprint(w, tt.response)
				},
			})
			stdout, stderr, code := runRevisionCLI(t, "restore", "sess_test", "--revision", "7", "--wait", "--timeout", "20ms", "--json")
			if code != 1 || !strings.Contains(stderr, tt.want) || posts.Load() != 1 {
				t.Fatalf("exit=%d stdout=%s stderr=%s posts=%d", code, stdout, stderr, posts.Load())
			}
			if tt.name == "timeout" {
				var receipt revisionActionReceipt
				if json.Unmarshal([]byte(stdout), &receipt) != nil || receipt.Operation == nil || receipt.Operation.ID != "op_exact" || receipt.ObservationError == "" || receipt.ResultRevision != nil {
					t.Fatalf("timeout lost operation receipt: %s", stdout)
				}
			}
		})
	}
}

func TestRevisionActionsRespectCapabilitiesWithoutFallback(t *testing.T) {
	for _, tt := range []struct {
		name, action, reason, snapshot string
		sequence                       int
	}{
		{"current", "restore", "current_revision", "available", 12},
		{"lost snapshot", "restore", "snapshot_lost", "lost", 7},
		{"capture gate", "redeploy", "snapshot_pending", "capturing", 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			revisionTestServer(t, map[string]http.HandlerFunc{
				"GET " + revisionTestPath: func(w http.ResponseWriter, r *http.Request) {
					revision := testRevision(tt.sequence)
					revision.RestoreCapability = cloud.RevisionCapability{Reason: &tt.reason}
					revision.Snapshot.Status = tt.snapshot
					_ = json.NewEncoder(w).Encode(cloud.RevisionPage{CurrentRevisionID: "rev_12", Revisions: []cloud.Revision{revision}})
				},
			})
			_, stderr, code := runRevisionCLI(t, tt.action, "sess_test", "--revision", strconv.Itoa(tt.sequence))
			if code != 1 || !strings.Contains(stderr, revisionReason(tt.reason)) {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
		})
	}
}

func TestWaitRevisionOperationTracksReceiptInsteadOfLatestDeployment(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			var polls, resultReads atomic.Int32
			server := revisionTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments/sess_test/operations/op_exact": func(w http.ResponseWriter, r *http.Request) {
					polls.Add(1)
					fmt.Fprint(w, `{"id":"op_exact","kind":"restore","status":"succeeded","result_revision_id":"rev_13"}`)
				},
				"GET " + revisionTestPath + "/rev_13": func(w http.ResponseWriter, r *http.Request) {
					if resultReads.Add(1) == 1 {
						http.NotFound(w, r)
						return
					}
					revision := testRevision(13)
					revision.Kind = "restore"
					parent, source := "rev_12", "rev_7"
					if mismatch {
						source = "rev_8"
					}
					revision.ParentRevisionID, revision.RestoredFromRevisionID = &parent, &source
					_ = json.NewEncoder(w).Encode(revision)
				},
			})
			client := cloud.NewClient(server.URL, "control-token")
			source := testRevision(7)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			operation, result, err := waitRevisionOperation(ctx, client, "sess_test", "restore", "rev_12", &source, &cloud.RevisionOperation{ID: "op_exact", Status: "running"}, time.Millisecond)
			if mismatch {
				if err == nil || !strings.Contains(err.Error(), "does not match") || result != nil {
					t.Fatalf("accepted mismatched result: %+v, %v", result, err)
				}
			} else if err != nil || result == nil || result.ID != "rev_13" || operation.Status != "succeeded" || polls.Load() != 2 {
				t.Fatalf("operation=%+v result=%+v err=%v polls=%d", operation, result, err, polls.Load())
			}
		})
	}
}

func TestWaitRevisionOperationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := testRevision(7)
	operation := &cloud.RevisionOperation{ID: "op_exact", Status: "running"}
	last, result, err := waitRevisionOperation(ctx, cloud.NewClient("http://unused.invalid", ""), "sess_test", "restore", "rev_12", &source, operation, time.Hour)
	if !errors.Is(err, context.Canceled) || result != nil || last != operation {
		t.Fatalf("cancellation lost receipt: %+v %+v %v", last, result, err)
	}
}

func TestRevisionMessageUnicodeLimit(t *testing.T) {
	message := strings.Repeat("界", 200)
	if got, err := normalizeCLIRevisionMessage(" " + message + " "); err != nil || got != message {
		t.Fatalf("valid Unicode message rejected: %v", err)
	}
	for _, invalid := range []string{message + "界", "a\nb", "a\tb", "a\x1bb", "a\u2028b"} {
		if _, err := normalizeCLIRevisionMessage(invalid); err == nil {
			t.Fatalf("accepted invalid message %q", invalid)
		}
	}
}
