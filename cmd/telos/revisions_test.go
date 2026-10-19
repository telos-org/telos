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

const revisionTestPath = "/api/deployments/goal_test/revisions"

func testRevision(sequence int) cloud.Revision {
	revision := cloud.Revision{
		ID: fmt.Sprintf("rev_%d", sequence), Sequence: sequence, Kind: "deploy",
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
		case "GET /api/deployments/goal_test":
			_ = json.NewEncoder(w).Encode(cloud.SessionRecord{ID: "goal_test", Name: "demo"})
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
	var pageReads, labelReads atomic.Int32
	ids := map[int]string{1000: "rev_current", 7: "rev_inspected", 6: "rev_parent", 2: "rev_source"}
	historyRevision := func(sequence int) cloud.Revision {
		revision := testRevision(sequence)
		if id := ids[sequence]; id != "" {
			revision.ID = id
		}
		return revision
	}
	routes := map[string]http.HandlerFunc{
		"GET " + revisionTestPath: func(w http.ResponseWriter, r *http.Request) {
			pageReads.Add(1)
			before, _ := strconv.Atoi(r.URL.Query().Get("before"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			start := 1000
			if before > 0 {
				start = before - 1
			}
			page := cloud.RevisionPage{CurrentRevisionID: "rev_current", Revisions: []cloud.Revision{}}
			for sequence := start; sequence > 0 && len(page.Revisions) < limit; sequence-- {
				page.Revisions = append(page.Revisions, historyRevision(sequence))
			}
			if len(page.Revisions) > 0 && page.Revisions[len(page.Revisions)-1].Sequence > 1 {
				cursor := page.Revisions[len(page.Revisions)-1].Sequence
				page.NextBefore = &cursor
			}
			_ = json.NewEncoder(w).Encode(page)
		},
		"GET " + revisionTestPath + "/rev_inspected": func(w http.ResponseWriter, r *http.Request) {
			revision := historyRevision(7)
			revision.Kind = "restore"
			parent, source := "rev_parent", "rev_source"
			revision.ParentRevisionID, revision.RestoredFromRevisionID = &parent, &source
			reason := "snapshot_lost"
			revision.RestoreCapability = cloud.RevisionCapability{Reason: &reason}
			_ = json.NewEncoder(w).Encode(revision)
		},
	}
	for _, sequence := range []int{1000, 6, 2} {
		routes["GET "+revisionTestPath+"/"+ids[sequence]] = func(w http.ResponseWriter, r *http.Request) {
			labelReads.Add(1)
			_ = json.NewEncoder(w).Encode(historyRevision(sequence))
		}
	}
	revisionTestServer(t, routes)
	stdout, stderr, code := runRevisionCLI(t, "history", "goal_test")
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "REV ") || !strings.Contains(stdout, "1000*") || !strings.Contains(stdout, "--before 951") || !strings.Contains(stdout, "Update 1000") || labelReads.Load() != 0 {
		t.Fatalf("history: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	pageReads.Store(0)
	stdout, stderr, code = runRevisionCLI(t, "history", "goal_test", "--revision", "7", "--json")
	var detail struct {
		GoalID   string         `json:"goal_id"`
		Revision cloud.Revision `json:"revision"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &detail) != nil || detail.GoalID != "goal_test" || detail.Revision.Sequence != 7 || detail.Revision.ID != "rev_inspected" || pageReads.Load() > 2 {
		t.Fatalf("old revision: exit=%d stdout=%s stderr=%s reads=%d", code, stdout, stderr, pageReads.Load())
	}
	stdout, stderr, code = runRevisionCLI(t, "history", "goal_test", "--revision", "rev_inspected")
	if code != 0 || !strings.Contains(stdout, "saved snapshot is no longer available") || !strings.Contains(strings.Join(strings.Fields(stdout), " "), "Redeploy available") {
		t.Fatalf("detail: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	for _, field := range []string{"Revision ID rev_inspected", "Current revision 1000", "Parent revision 6", "Source revision 2"} {
		if !strings.Contains(strings.Join(strings.Fields(stdout), " "), field) {
			t.Fatalf("detail missing %q: %s", field, stdout)
		}
	}
	labelReads.Store(0)
	stdout, stderr, code = runRevisionCLI(t, "history", "goal_test", "--before", "10", "--limit", "3")
	if code != 0 || stderr != "" || strings.Contains(stdout, "Current revision") || !strings.Contains(stdout, "Update 9") || !strings.Contains(stdout, "--before 7") || strings.Contains(stdout, "Update 1000") || strings.Contains(stdout, "rev_") || labelReads.Load() != 0 {
		t.Fatalf("older page: exit=%d stdout=%s stderr=%s lookups=%d", code, stdout, stderr, labelReads.Load())
	}
	labelReads.Store(0)
	stdout, stderr, code = runRevisionCLI(t, "history", "goal_test", "--before", "10", "--limit", "3", "--json")
	var older cloud.RevisionPage
	if code != 0 || json.Unmarshal([]byte(stdout), &older) != nil || older.CurrentRevisionID != "rev_current" || len(older.Revisions) != 3 || older.Revisions[0].Sequence != 9 || labelReads.Load() != 0 {
		t.Fatalf("older JSON page: exit=%d stdout=%s stderr=%s lookups=%d", code, stdout, stderr, labelReads.Load())
	}
	stdout, stderr, code = runRevisionCLI(t, "history", "goal_test", "--all", "--limit", "20", "--json")
	var all cloud.RevisionPage
	if code != 0 || json.Unmarshal([]byte(stdout), &all) != nil || len(all.Revisions) != 1000 || all.NextBefore != nil {
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
		stdout, stderr, code := runRevisionCLI(t, append([]string{"history", "goal_test"}, flags...)...)
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
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"history", "local_test"}, "requires a Cloud Goal ID"},
		{[]string{"history", "goal_test", "--limit", "101"}, "--limit must be between 1 and 100"},
		{[]string{"history", "goal_test", "--before", "0"}, "--before must be positive"},
		{[]string{"history", "goal_test", "--revision", "0"}, "revision must be a positive number"},
		{[]string{"history", "goal_test", "--revision", "7", "--all"}, "--revision cannot be combined"},
		{[]string{"diff", "goal_test", "HEAD"}, "revision must be a positive number"},
		{[]string{"restore", "goal_test", "--message", "Recovery"}, "revision must be a positive number"},
		{[]string{"restore", "goal_test", "--revision", "7", "--message", "Recovery", "--timeout", "1s"}, "--timeout must be positive and requires --wait"},
		{[]string{"redeploy", "goal_test", "--revision", "7", "--message", "Recovery", "--wait", "--timeout", "0s"}, "--timeout must be positive and requires --wait"},
		{[]string{"restore", "goal_test", "--revision", "7"}, "--message is required"},
		{[]string{"redeploy", "goal_test", "--revision", "7", "--message", ""}, "--message is required"},
		{[]string{"restore", "goal_test", "--revision", "7", "--message", "   "}, "--message is required"},
		{[]string{"restore", "goal_test", "--revision", "7", "--message", "two\nlines"}, "revision messages must be a single line"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			stdout, stderr, code := runRevisionCLI(t, tt.args...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, tt.want) {
				t.Fatalf("exit=%d stdout=%s stderr=%s; want %q", code, stdout, stderr, tt.want)
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
				args := []string{action, "goal_test", "--revision", "7", "--message", " Back to stable ", "--json"}
				if wait {
					args = append(args, "--wait")
				}
				stdout, stderr, code := runRevisionCLI(t, args...)
				var receipt revisionActionReceipt
				if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &receipt) != nil || receipt.GoalID != "goal_test" || receipt.Operation == nil || receipt.Operation.ID != "op_exact" || posts.Load() != 1 {
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
		name, action, response, want string
		status                       int
	}{
		{"stale", "restore", `{"error":{"code":"stale_current_revision","message":"Current revision changed"}}`, "restore revision 7: Current revision changed", 409},
		{"access differs", "restore", `{"detail":{"code":"restore_access_differs","message":"Redeploy the revision to apply its access, then restore its snapshot."}}`, "restore revision 7: Redeploy the revision to apply its access, then restore its snapshot.", 409},
		{"unsupported access", "redeploy", `{"detail":{"code":"access_unsupported","message":"Host cannot enforce the requested policy"}}`, "redeploy revision 7: Host cannot enforce the requested policy", 409},
		{"access changed", "redeploy", `{"detail":{"code":"access_changed","message":"Deployment access changed during validation. Submit the update again."}}`, "redeploy revision 7: Deployment access changed during validation. Submit the update again.", 409},
		{"dispatch failure after commit", "restore", `{"detail":"Runtime update rejected"}`, "check history before retrying", 409},
		{"server failure", "restore", `{"error":{"code":"internal_error","message":"Lost response"}}`, "check history before retrying", 503},
		{"protected", "restore", `{"detail":{"code":"change_requests_client_required","message":"Use change requests"}}`, "use the Telos web UI", 409},
		{"failed operation", "restore", `{"id":"op_exact","status":"failed","error":{"code":"snapshot_lost","message":"Snapshot disappeared"}}`, "Snapshot disappeared", 202},
		{"timeout", "restore", `{"id":"op_exact","status":"running"}`, "the Cloud operation was not cancelled", 202},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var posts atomic.Int32
			revisionTestServer(t, map[string]http.HandlerFunc{
				"POST " + revisionTestPath + "/rev_7/" + tt.action: func(w http.ResponseWriter, r *http.Request) {
					posts.Add(1)
					w.WriteHeader(tt.status)
					if tt.name == "failed operation" {
						fmt.Fprint(w, `{"id":"op_exact","status":"running"}`)
					} else {
						fmt.Fprint(w, tt.response)
					}
				},
				"GET /api/deployments/goal_test/operations/op_exact": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, tt.response)
				},
			})
			timeout := "5s"
			if tt.name == "timeout" {
				timeout = "20ms"
			}
			stdout, stderr, code := runRevisionCLI(t, tt.action, "goal_test", "--revision", "7", "--message", "Back to stable", "--wait", "--timeout", timeout, "--json")
			if code != 1 || !strings.Contains(stderr, tt.want) || posts.Load() != 1 {
				t.Fatalf("exit=%d stdout=%s stderr=%s posts=%d", code, stdout, stderr, posts.Load())
			}
			if tt.status == http.StatusAccepted {
				var receipt revisionActionReceipt
				if json.Unmarshal([]byte(stdout), &receipt) != nil || receipt.Operation == nil || receipt.Operation.ID != "op_exact" || receipt.ResultRevision != nil {
					t.Fatalf("lost operation receipt: %s", stdout)
				}
				if tt.name == "timeout" && receipt.ObservationError == "" {
					t.Fatalf("missing observation error: %s", stdout)
				}
				if tt.name == "failed operation" && (receipt.Operation.Status != "failed" || receipt.Operation.Error == nil || receipt.Operation.Error.Code != "snapshot_lost" || receipt.ObservationError != "") {
					t.Fatalf("incorrect failure receipt: %s", stdout)
				}
			}
		})
	}
}

func TestRevisionActionsRespectCapabilitiesWithoutFallback(t *testing.T) {
	for _, tt := range []struct {
		name, action, reason, snapshot, want string
		sequence                             int
		enabled                              bool
	}{
		{"current", "restore", "current_revision", "available", "this revision is already current", 12, true},
		{"lost snapshot", "restore", "snapshot_lost", "lost", "the saved snapshot is no longer available", 7, true},
		{"capture gate", "redeploy", "snapshot_pending", "capturing", "a snapshot is still being captured", 7, true},
		{"unsupported Cloud", "restore", "", "available", "not supported by this Cloud release", 7, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			revisionTestServer(t, map[string]http.HandlerFunc{
				"GET /api/capabilities": func(w http.ResponseWriter, r *http.Request) {
					capabilities := testRevisionCapabilities()
					capabilities.DeploymentSnapshotRestore = tt.enabled
					_ = json.NewEncoder(w).Encode(capabilities)
				},
				"GET " + revisionTestPath: func(w http.ResponseWriter, r *http.Request) {
					revision := testRevision(tt.sequence)
					if tt.reason != "" {
						revision.RestoreCapability = cloud.RevisionCapability{Reason: &tt.reason}
					}
					revision.Snapshot.Status = tt.snapshot
					_ = json.NewEncoder(w).Encode(cloud.RevisionPage{CurrentRevisionID: "rev_12", Revisions: []cloud.Revision{revision}})
				},
			})
			_, stderr, code := runRevisionCLI(t, tt.action, "goal_test", "--revision", strconv.Itoa(tt.sequence), "--message", "Back to stable")
			if code != 1 || !strings.Contains(stderr, tt.want) {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
		})
	}
}

func TestWaitRevisionOperationTracksReceiptInsteadOfLatestDeployment(t *testing.T) {
	for _, mismatch := range []string{"matching", "kind", "source", "parent", "digest"} {
		t.Run(mismatch, func(t *testing.T) {
			var resultReads atomic.Int32
			server := revisionTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments/goal_test/operations/op_exact": func(w http.ResponseWriter, r *http.Request) {
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
					switch mismatch {
					case "kind":
						revision.Kind = "redeploy"
					case "source":
						source = "rev_8"
					case "parent":
						parent = "rev_11"
					case "digest":
						revision.PackageDigest = "sha256:other"
					}
					revision.ParentRevisionID, revision.RestoredFromRevisionID = &parent, &source
					_ = json.NewEncoder(w).Encode(revision)
				},
			})
			client := cloud.NewClient(server.URL, "control-token")
			source := testRevision(7)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			operation, result, err := waitRevisionOperation(ctx, client, "goal_test", "restore", "rev_12", &source, &cloud.RevisionOperation{ID: "op_exact", Status: "running"}, time.Millisecond)
			if mismatch != "matching" {
				if err == nil || !strings.Contains(err.Error(), "does not match") || result != nil {
					t.Fatalf("accepted mismatched result: %+v, %v", result, err)
				}
			} else if err != nil || result == nil || result.ID != "rev_13" || operation.Status != "succeeded" {
				t.Fatalf("operation=%+v result=%+v err=%v", operation, result, err)
			}
		})
	}
}

func TestWaitRevisionOperationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := testRevision(7)
	operation := &cloud.RevisionOperation{ID: "op_exact", Status: "running"}
	last, result, err := waitRevisionOperation(ctx, cloud.NewClient("http://unused.invalid", ""), "goal_test", "restore", "rev_12", &source, operation, time.Hour)
	if !errors.Is(err, context.Canceled) || result != nil || last != operation {
		t.Fatalf("cancellation lost receipt: %+v %+v %v", last, result, err)
	}
}

func TestRevisionMessageUnicodeLimit(t *testing.T) {
	message := strings.Repeat("界", 200)
	if got, err := normalizeCLIRevisionMessage("\ufeff " + message + " \ufeff"); err != nil || got != message {
		t.Fatalf("valid Unicode message rejected: %v", err)
	}
	for _, invalid := range []string{"", "   ", "\u2003\u00a0", "\ufeff \ufeff", message + "界", "a\nb", "a\tb", "a\x1bb", "a\u2028b"} {
		if _, err := normalizeCLIRevisionMessage(invalid); err == nil {
			t.Fatalf("accepted invalid message %q", invalid)
		}
	}
}
