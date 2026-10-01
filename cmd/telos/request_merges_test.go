package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func mergeText(content string) *cloud.MergeFile {
	return &cloud.MergeFile{Content: &content, Mode: "0644"}
}

func testRequestMergeWorkspace() requestMergeWorkspace {
	current := "rev_8"
	return requestMergeWorkspace{
		Version: 1, Context: "personal", OrgID: "org_personal", APIEndpoint: "https://api.example.com", DeploymentID: "sess_123",
		Merge: cloud.RequestMerge{
			MergeID: "merge_exact", RequestID: "cr_saved", UpdateNumber: 1, PreparedPlanID: "cp_first",
			CurrentRevisionID: &current,
			Files:             []cloud.RequestMergeFile{{Path: "SPEC.md", Base: mergeText("base\n"), Current: mergeText("current\n"), Proposed: mergeText("proposed\n"), Merged: mergeText("<<<<<<< current\ncurrent\n=======\nproposed\n>>>>>>> proposed\n"), ConflictIDs: []string{"conflict_spec"}}},
			Conflicts:         []cloud.MergeConflict{{ID: "conflict_spec", Path: "SPEC.md", Kind: "content"}},
		},
		Resolutions: []cloud.MergeResolution{{ConflictID: "conflict_spec"}},
	}
}

func TestMergeWorkspaceRequiresExplicitConflictChoices(t *testing.T) {
	workspace := testRequestMergeWorkspace()
	directory := filepath.Join(t.TempDir(), "merge")
	if err := writeRequestMergeWorkspace(directory, workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "merged/SPEC.md"), []byte("resolved without markers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(directory)
	defer root.Close()
	_, err := requestMergeEdits(root, &workspace)
	var conflictError *requestMergeError
	if !errors.As(err, &conflictError) || conflictError.code != "merge_conflicts" {
		t.Fatalf("marker removal silently resolved the conflict: %v", err)
	}
	workspace.Resolutions[0].Choice = "merged"
	options, err := requestMergeEdits(root, &workspace)
	if err != nil || len(options.Resolutions) != 1 || options.Resolutions[0].Content == nil || *options.Resolutions[0].Content != "resolved without markers\n" || options.MergeID != "merge_exact" {
		t.Fatalf("resolution=%+v err=%v", options, err)
	}
	workspace.Resolutions[0].Choice = "current"
	options, err = requestMergeEdits(root, &workspace)
	if err != nil || len(options.Files) != 0 || options.Resolutions[0].Content != nil {
		t.Fatalf("draft overwrote explicit source choice: %+v err=%v", options, err)
	}
	workspace.Resolutions[0].Choice = "merged"
	_ = root.Remove("merged/SPEC.md")
	options, err = requestMergeEdits(root, &workspace)
	if err != nil || options.Resolutions[0].Content != nil || len(options.Files) != 1 || options.Files[0].Content != nil {
		t.Fatalf("deletion resolution=%+v err=%v", options, err)
	}
}

func TestMergeWorkspacePreservesBinaryAndIncludesPackageEdits(t *testing.T) {
	workspace := testRequestMergeWorkspace()
	workspace.Merge.Conflicts, workspace.Resolutions = nil, nil
	workspace.Merge.Files[0].Merged = mergeText("merged\n")
	binary := base64.StdEncoding.EncodeToString([]byte{0, 1, 255})
	binaryFile := &cloud.MergeFile{DataBase64: &binary, Mode: "0644"}
	script := mergeText("#!/bin/sh\ntrue\n")
	script.Mode = "0755"
	workspace.Merge.Files = append(workspace.Merge.Files,
		cloud.RequestMergeFile{Path: "skills/check/assets/status.png", Merged: binaryFile},
		cloud.RequestMergeFile{Path: "skills/check/scripts/run.sh", Merged: script},
		cloud.RequestMergeFile{Path: ".telos/skills/check.json", Kind: "skill_metadata", Merged: mergeText("{\"required\":true}\n")},
	)
	directory := filepath.Join(t.TempDir(), "merge")
	if err := writeRequestMergeWorkspace(directory, workspace); err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(directory)
	defer root.Close()
	options, err := requestMergeEdits(root, &workspace)
	if err != nil || len(options.Files) != 0 {
		t.Fatalf("unchanged package altered: %+v err=%v", options, err)
	}
	_ = root.WriteFile("merged/skills/check/scripts/run.sh", []byte("#!/bin/sh\nfalse\n"), 0o700)
	_ = root.WriteFile("merged/skills/check/README.md", []byte("new doc\n"), 0o600)
	_ = root.Remove("merged/.telos/skills/check.json")
	options, err = requestMergeEdits(root, &workspace)
	if err != nil || len(options.Files) != 3 || options.Files[0].Content != nil || options.Files[2].Mode != "0755" {
		t.Fatalf("package edits=%+v err=%v", options, err)
	}
	_ = root.WriteFile("merged/skills/check/assets/status.png", []byte{0, 1, 2}, 0o600)
	if _, err := requestMergeEdits(root, &workspace); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("custom binary edit accepted: %v", err)
	}
}

func TestMergeWorkspaceRejectsUnsafePathsAndLinks(t *testing.T) {
	for _, path := range []string{"../SPEC.md", "/SPEC.md", "skills/../../outside", "skills/a/../x", "skills/a/\\outside", ".git/config"} {
		t.Run(path, func(t *testing.T) {
			workspace := testRequestMergeWorkspace()
			workspace.Merge.Files[0].Path = path
			directory := filepath.Join(t.TempDir(), "merge")
			if err := writeRequestMergeWorkspace(directory, workspace); err == nil {
				t.Fatal("unsafe path accepted")
			}
			if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed export left partial tree: %v", err)
			}
		})
	}
	workspace := testRequestMergeWorkspace()
	workspace.Resolutions[0].Choice = "merged"
	directory := filepath.Join(t.TempDir(), "merge")
	if err := writeRequestMergeWorkspace(directory, workspace); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(directory, "merged/SPEC.md"))
	_ = os.Symlink(filepath.Join(directory, "base/SPEC.md"), filepath.Join(directory, "merged/SPEC.md"))
	root, _ := os.OpenRoot(directory)
	defer root.Close()
	if _, err := requestMergeEdits(root, &workspace); err == nil {
		t.Fatal("symlink accepted as a resolution")
	}
}

func TestReconcileAndResolvePinIdentitiesAndNeverApply(t *testing.T) {
	workspace := testRequestMergeWorkspace()
	var prepares, resolutions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		request := testDeploymentPlan("apply", "awaiting_confirmation")
		request.CanEdit, request.PlanStale, request.UpdateNumber = true, true, 1
		request.CurrentRevisionID = workspace.Merge.CurrentRevisionID
		switch r.Method + " " + r.URL.Path {
		case "GET /api/change-requests/cr_saved":
			_ = json.NewEncoder(w).Encode(request)
		case "GET /api/deployments/sess_123/change-requests/cr_saved/merge":
			prepares++
			if r.URL.Query().Get("expected_update_number") != "1" || r.URL.Query().Get("expected_current_revision_id") != "rev_8" {
				t.Errorf("merge query lost identity: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(workspace.Merge)
		case "POST /api/deployments/sess_123/change-requests/cr_saved/merge":
			resolutions++
			var options cloud.RequestMergeOptions
			_ = json.NewDecoder(r.Body).Decode(&options)
			if options.MergeID != "merge_exact" || options.ExpectedUpdateNumber != 1 || options.ExpectedCurrentRevisionID == nil || *options.ExpectedCurrentRevisionID != "rev_8" || len(options.Resolutions) != 1 || options.Resolutions[0].Choice != "current" {
				t.Errorf("lost merge identity: %+v", options)
			}
			request.UpdateNumber, request.PreparedPlanID, request.PlanStale = 2, "cp_second", false
			_ = json.NewEncoder(w).Encode(request)
		default:
			t.Errorf("unexpected upload/apply: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	directory := filepath.Join(t.TempDir(), "merge")
	err := runCloudRequestReconcile("cr_saved", directory, "", true)
	var mergeError *requestMergeError
	if !errors.As(err, &mergeError) || mergeError.code != "merge_conflicts" {
		t.Fatalf("err=%v", err)
	}
	data, _ := os.ReadFile(filepath.Join(directory, requestMergeManifest))
	_ = json.Unmarshal(data, &workspace)
	workspace.Resolutions[0].Choice = "current"
	data, _ = json.Marshal(workspace)
	_ = os.WriteFile(filepath.Join(directory, requestMergeManifest), data, 0o600)
	output := filepath.Join(t.TempDir(), "updated.plan")
	captureStdout(t, func() {
		if err := runCloudRequestResolve("cr_saved", directory, "", "", output, true); err != nil {
			t.Fatal(err)
		}
	})
	saved, err := readSavedDeploymentPlan(output)
	if err != nil || saved.Version != 2 || saved.UpdateNumber != 2 || saved.PreparedPlanID != "cp_second" || prepares != 1 || resolutions != 1 {
		t.Fatalf("saved=%+v err=%v prepares=%d resolves=%d", saved, err, prepares, resolutions)
	}
}

func TestStaleUpdateRequiresMergeBeforeUploading(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/change-requests/cr_saved" {
			t.Errorf("stale update uploaded: %s %s", r.Method, r.URL.Path)
		}
		request := testDeploymentPlan("saved", "awaiting_confirmation")
		request.UpdateNumber, request.CanEdit, request.PlanStale = 1, true, true
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	err := runCloudRequestUpdate("missing-SPEC.md", "cr_saved", "", "", "", true)
	var mergeError *requestMergeError
	if !errors.As(err, &mergeError) || mergeError.code != "merge_required" {
		t.Fatalf("err=%v", err)
	}
}

func TestReconcileCLIConflictReceiptIsJSONAndNonzero(t *testing.T) {
	workspace := testRequestMergeWorkspace()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if strings.HasSuffix(r.URL.Path, "/merge") {
			_ = json.NewEncoder(w).Encode(workspace.Merge)
			return
		}
		request := testDeploymentPlan("saved", "awaiting_confirmation")
		request.UpdateNumber, request.CanEdit, request.CurrentRevisionID = 1, true, workspace.Merge.CurrentRevisionID
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	directory := filepath.Join(t.TempDir(), "merge")
	args, _ := json.Marshal([]string{"plan", "--request", "cr_saved", "--reconcile", directory, "--json"})
	command := exec.Command(os.Args[0], "-test.run=^TestCLIPlanApplySubprocess$")
	command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(args))
	output, err := command.Output()
	var receipt struct {
		Error     struct{ Code string } `json:"error"`
		Workspace string                `json:"workspace"`
	}
	if err == nil || json.Unmarshal(output, &receipt) != nil || receipt.Error.Code != "merge_conflicts" || receipt.Workspace != directory {
		t.Fatalf("err=%v output=%s", err, output)
	}
}
