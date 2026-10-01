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
	"github.com/telos-org/telos/internal/spec"
)

func mergeText(value string) *cloud.MergeFile { return &cloud.MergeFile{Content: &value, Mode: "0644"} }
func testRequestMergeWorkspace(t *testing.T) requestMergeWorkspace {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current := "rev_8"
	original := strings.Replace(testPlanSpec, "Serve a demo.", "Serve my local proposal.", 1)
	marked := strings.Replace(testPlanSpec, "Serve a demo.", "<<<<<<< Current deployment\nServe the current deployment.\n||||||| Original\nServe a demo.\n=======\nServe my local proposal.\n>>>>>>> Your proposed changes", 1)
	if err := os.WriteFile(filepath.Join(directory, "SPEC.md"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := requestMergeWorkspace{Version: 1, Root: directory, SpecName: "SPEC.md", Context: "personal", OrgID: "org_personal", APIEndpoint: "https://api.example.com", DeploymentID: "sess_123", PackageRef: "@personal/candidate:1.0.0", SkillPaths: map[string]string{},
		Merge: cloud.RequestMerge{MergeID: "merge_exact", RequestID: "cr_saved", UpdateNumber: 1, PreparedPlanID: "cp_first", CurrentRevisionID: &current,
			Files:     []cloud.RequestMergeFile{{Path: "SPEC.md", Base: mergeText(testPlanSpec), Current: mergeText(testPlanSpec), Proposed: mergeText(original), Merged: mergeText(marked), ConflictIDs: []string{"conflict_spec"}}},
			Conflicts: []cloud.MergeConflict{{ID: "conflict_spec", Path: "SPEC.md", Kind: "content"}}}}
	workspace.Original, err = workspace.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}
func installTestMerge(t *testing.T, w *requestMergeWorkspace) {
	t.Helper()
	files := map[string]*cloud.MergeFile{}
	for _, file := range w.Merge.Files {
		files[file.Path] = file.Merged
	}
	if err := w.materialize(files, true); err != nil {
		t.Fatal(err)
	}
	w.Materialized = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
}
func TestRequestTextConflictUsesNormalFilesAndRerun(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	installTestMerge(t, &workspace)
	_, _, err := requestMergeEdits(&workspace, nil)
	var conflict *requestMergeError
	if !errors.As(err, &conflict) || conflict.code != "merge_conflicts" {
		t.Fatalf("unedited conflict accepted: %v", err)
	}
	resolved := strings.Replace(testPlanSpec, "Serve a demo.", "Serve the resolved proposal.", 1)
	if err := os.WriteFile(filepath.Join(workspace.Root, "SPEC.md"), []byte(resolved), 0o600); err != nil {
		t.Fatal(err)
	}
	options, files, err := requestMergeEdits(&workspace, nil)
	if err != nil || len(options.Resolutions) != 1 || options.Resolutions[0].Choice != "merged" || options.PackageRef != workspace.PackageRef || options.MergeID != "merge_exact" || files["SPEC.md"].Content == nil || *files["SPEC.md"].Content != resolved {
		t.Fatalf("options=%+v files=%v err=%v", options, files, err)
	}
}
func TestRequestNonTextConflictsRequireExplicitChoices(t *testing.T) {
	for _, kind := range []string{"add_add", "delete_modify", "binary", "mode", "metadata", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			workspace := testRequestMergeWorkspace(t)
			workspace.Merge.Conflicts[0].Kind = kind
			workspace.Merge.Files[0].Merged = mergeText(testPlanSpec)
			installTestMerge(t, &workspace)
			_, _, err := requestMergeEdits(&workspace, nil)
			var conflict *requestMergeError
			if !errors.As(err, &conflict) || conflict.code != "merge_conflicts" {
				t.Fatalf("%s silently resolved: %v", kind, err)
			}
			options, files, err := requestMergeEdits(&workspace, []requestConflictChoice{{"SPEC.md", "current"}})
			if err != nil || options.Resolutions[0].Choice != "current" || !sameMergeFile(files["SPEC.md"], workspace.Merge.Files[0].Current) {
				t.Fatalf("options=%+v err=%v", options, err)
			}
		})
	}
}
func TestRequestBinaryResolutionPreservesBytes(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	workspace.Merge.Conflicts = nil
	workspace.Merge.Files[0].Merged = mergeText(testPlanSpec)
	encoded := base64.StdEncoding.EncodeToString([]byte{0, 1, 255})
	binary := &cloud.MergeFile{DataBase64: &encoded, Mode: "0644"}
	workspace.SkillPaths["check"] = "custom/check"
	workspace.Merge.Files = append(workspace.Merge.Files, cloud.RequestMergeFile{Path: "skills/check/icon.png", Base: binary, Current: binary, Proposed: binary, Merged: binary, ConflictIDs: []string{"icon"}}, cloud.RequestMergeFile{Path: ".telos/skills/check.json", Kind: "skill_metadata", Merged: mergeText("{\"required\":false}\n")})
	workspace.Merge.Conflicts = []cloud.MergeConflict{{ID: "icon", Path: "skills/check/icon.png", Kind: "binary"}}
	installTestMerge(t, &workspace)
	if _, _, err := requestMergeEdits(&workspace, []requestConflictChoice{{"skills/check/icon.png", "local"}}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary custom accepted: %v", err)
	}
	_, files, err := requestMergeEdits(&workspace, []requestConflictChoice{{"skills/check/icon.png", "proposed"}})
	if err != nil || !sameMergeFile(files["skills/check/icon.png"], binary) {
		t.Fatalf("binary=%+v err=%v", files, err)
	}
}
func TestRequestMergeRejectsPathsSymlinksAndLocalRaces(t *testing.T) {
	for _, name := range []string{"../SPEC.md", "/SPEC.md", "skills/../../outside", "skills/a/../x", "skills/a/\\outside", ".git/config"} {
		if validMergePath(name) {
			t.Errorf("unsafe path %q accepted", name)
		}
	}
	workspace := testRequestMergeWorkspace(t)
	before, _ := os.ReadFile(filepath.Join(workspace.Root, "SPEC.md"))
	_ = os.WriteFile(filepath.Join(workspace.Root, "SPEC.md"), []byte("another editor changed this"), 0o600)
	if err := workspace.materialize(map[string]*cloud.MergeFile{"SPEC.md": mergeText(testPlanSpec)}, true); err == nil {
		t.Fatal("concurrent local edit overwritten")
	}
	data, _ := os.ReadFile(filepath.Join(workspace.Root, "SPEC.md"))
	if string(data) != "another editor changed this" {
		t.Fatal("local edit lost")
	}
	_ = os.Remove(filepath.Join(workspace.Root, "SPEC.md"))
	outside := filepath.Join(t.TempDir(), "outside")
	_ = os.WriteFile(outside, before, 0o600)
	_ = os.Symlink(outside, filepath.Join(workspace.Root, "SPEC.md"))
	if _, err := workspace.snapshot(); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestRequestMergeSynchronizesCustomSkillPathsAndModes(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	workspace.Merge.Conflicts = nil
	workspace.SkillPaths = map[string]string{"check": "my-skills/check"}
	workspace.Merge.Files[0].Merged = mergeText(testPlanSpec)
	script := mergeText("#!/bin/sh\ntrue\n")
	script.Mode = "0755"
	files := map[string]*cloud.MergeFile{"SPEC.md": mergeText(testPlanSpec), "skills/check/SKILL.md": mergeText("---\nname: check\n---\nCheck status.\n"), "skills/check/run.sh": script, ".telos/skills/check.json": mergeText("{\"required\":true}\n")}
	if err := workspace.materialize(files, true); err != nil {
		t.Fatal(err)
	}
	markdown, _ := os.ReadFile(filepath.Join(workspace.Root, "SPEC.md"))
	if !strings.Contains(string(markdown), "./my-skills/check*") {
		t.Fatalf("local ref missing: %s", markdown)
	}
	info, err := os.Stat(filepath.Join(workspace.Root, "my-skills/check/run.sh"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("mode lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root, "skills/check/run.sh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canonical path wrote wrong local directory")
	}
}
func TestMergeSpecRewritePreservesBodyAndUnrelatedFrontmatter(t *testing.T) {
	source := "---\n# keep comment\nname: demo\nversion: 1.2.3\nskills: [\"@telos/check:1.0.0\"]\nplatform: cloud # keep too\n---\n\n- one\n- two\n"
	result, err := rewriteMergeSpec(source, map[string]string{"check": "custom/check"}, map[string]bool{"check": true})
	if err != nil || !strings.Contains(result, "# keep comment\nname: demo\nversion: 1.2.3\n") || !strings.HasSuffix(result, "platform: cloud # keep too\n---\n\n- one\n- two\n") {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func TestAutomaticRequestPlanCleanAndConflicted(t *testing.T) {
	for _, conflicted := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "conflicted"}[conflicted], func(t *testing.T) {
			workspace := testRequestMergeWorkspace(t)
			finalSpec := strings.Replace(testPlanSpec, "Serve a demo.", "Serve my local proposal.\nKeep the deployed audit log.", 1)
			if !conflicted {
				workspace.Merge.Conflicts = nil
				workspace.Merge.Files[0].ConflictIDs = nil
				workspace.Merge.Files[0].Merged = mergeText(finalSpec)
			}
			current := testDeploymentPlan("saved", "awaiting_confirmation")
			current.UpdateNumber, current.PreparedPlanID, current.CanEdit, current.PlanStale = 1, "cp_first", true, true
			current.CurrentRevisionID = workspace.Merge.CurrentRevisionID
			var prepares, resolves, uploads int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/deployment-plans/packages" {
					uploads++
				}
				if serveDeploymentPlanPrerequisites(w, r) {
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/change-requests/cr_saved":
					_ = json.NewEncoder(w).Encode(current)
				case "POST /api/deployments/sess_123/change-requests/cr_saved/merge/prepare":
					prepares++
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["package_ref"] != "@personal/plan-artifact:1.0.0" || body["expected_update_number"] != float64(1) || body["expected_current_revision_id"] != "rev_8" {
						t.Errorf("body=%+v", body)
					}
					_ = json.NewEncoder(w).Encode(workspace.Merge)
				case "POST /api/deployments/sess_123/change-requests/cr_saved/merge":
					resolves++
					var body cloud.RequestMergeOptions
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.MergeID != "merge_exact" || body.PackageRef != "@personal/plan-artifact:1.0.0" || body.ExpectedUpdateNumber != 1 || body.ExpectedCurrentRevisionID == nil || *body.ExpectedCurrentRevisionID != "rev_8" {
						t.Errorf("options=%+v", body)
					}
					current.UpdateNumber, current.PreparedPlanID, current.PlanStale = 2, "cp_second", false
					current.Preview.ProposedSpec = finalSpec
					_ = json.NewEncoder(w).Encode(current)
				default:
					t.Errorf("unexpected apply/mutation: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			control, err := cloud.ControlClientForContext("")
			if err != nil {
				t.Fatal(err)
			}
			specPath := filepath.Join(workspace.Root, "SPEC.md")
			if err := recordRequestWorkspace(control, &current, specPath); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "updated.plan")
			var runErr error
			captureStdout(t, func() { runErr = runCloudRequestUpdate(specPath, "cr_saved", "", "", output, true) })
			if conflicted {
				var mergeError *requestMergeError
				if !errors.As(runErr, &mergeError) || mergeError.code != "merge_conflicts" || resolves != 0 {
					t.Fatalf("err=%v resolves=%d", runErr, resolves)
				}
				data, _ := os.ReadFile(specPath)
				if !strings.Contains(string(data), "<<<<<<< Current deployment") {
					t.Fatal("no local conflict markers")
				}
				_ = os.WriteFile(specPath, []byte(finalSpec), 0o600)
				captureStdout(t, func() { runErr = runCloudRequestUpdate(specPath, "cr_saved", "", "", output, true) })
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			saved, err := readSavedDeploymentPlan(output)
			if err != nil || saved.Version != 2 || saved.UpdateNumber != 2 || saved.PreparedPlanID != "cp_second" || prepares != 1 || resolves != 1 || uploads != 1 {
				t.Fatalf("saved=%+v err=%v prepare=%d resolve=%d upload=%d", saved, err, prepares, resolves, uploads)
			}
			local, _ := os.ReadFile(specPath)
			if string(local) != finalSpec {
				t.Fatalf("deployment changes not retained locally: %s", local)
			}
		})
	}
}
func TestUnknownAndNewerRequestAncestryDoesNotUpload(t *testing.T) {
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID, request.CanEdit = 2, "cp_second", true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method != "GET" || r.URL.Path != "/api/change-requests/cr_saved" {
			t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	path := filepath.Join(t.TempDir(), "SPEC.md")
	_ = os.WriteFile(path, []byte(testPlanSpec), 0o600)
	err := runCloudRequestUpdate(path, "cr_saved", "", "", "", true)
	if err == nil || !strings.Contains(err.Error(), "telos get") {
		t.Fatalf("untracked update accepted: %v", err)
	}
	control, _ := cloud.ControlClientForContext("")
	old := request
	old.UpdateNumber = 1
	old.PreparedPlanID = "cp_first"
	if err := recordRequestWorkspace(control, &old, path); err != nil {
		t.Fatal(err)
	}
	err = runCloudRequestUpdate(path, "cr_saved", "", "", "", true)
	if err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("unseen remote update overwritten: %v", err)
	}
}
func TestConflictFlagAcceptsPathsNotWorkspaceDirectories(t *testing.T) {
	var flags requestConflictChoices
	for _, value := range []string{"./merge", "../SPEC.md=current", "SPEC.md=unknown"} {
		if err := flags.Set(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	if err := flags.Set("skills/check/icon.png=current"); err != nil {
		t.Fatal(err)
	}
	if err := flags.Set("skills/check/icon.png=proposed"); err == nil {
		t.Fatal("duplicate choices accepted")
	}
}
func TestRequestUnknownAncestryJSONExit(t *testing.T) {
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID, request.CanEdit = 1, "cp_first", true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	path := filepath.Join(t.TempDir(), "SPEC.md")
	_ = os.WriteFile(path, []byte(testPlanSpec), 0o600)
	args, _ := json.Marshal([]string{"plan", path, "--request", "cr_saved", "--json"})
	command := exec.Command(os.Args[0], "-test.run=^TestCLIPlanApplySubprocess$")
	command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(args))
	output, err := command.Output()
	var result struct {
		Error struct{ Code string } `json:"error"`
	}
	if err == nil || json.Unmarshal(output, &result) != nil || result.Error.Code != "untracked_request" {
		t.Fatalf("output=%s err=%v", output, err)
	}
}

func TestMergeFrontmatterFailsClosedForFlowAndInheritedKeys(t *testing.T) {
	for _, source := range []string{"---\n{name: demo, version: 1.2.3, skills: []}\n---\nBody\n", "---\nbase: &base\n  skills: []\n<<: *base\nname: demo\n---\nBody\n", "---\nname: demo\nskills: &shared []\ntags: *shared\n---\nBody\n"} {
		if result, err := rewriteMergeSpec(source, map[string]string{}, map[string]bool{}); err == nil {
			t.Fatalf("unsupported header rewritten: %s", result)
		}
	}
}
func TestResolvedSpecSkillSelectionIsPreserved(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstar", true: "remove"}[remove], func(t *testing.T) {
			w := testRequestMergeWorkspace(t)
			w.Merge.Conflicts = nil
			w.SkillPaths = map[string]string{"check": "custom/check"}
			w.Merge.Files[0].Merged = mergeText(testPlanSpec)
			w.Merge.Files = append(w.Merge.Files, cloud.RequestMergeFile{Path: "skills/check/SKILL.md", Merged: mergeText("---\nname: check\n---\nInspect state.\n")}, cloud.RequestMergeFile{Path: ".telos/skills/check.json", Kind: "skill_metadata", Merged: mergeText("{\"required\":true}\n")})
			installTestMerge(t, &w)
			data, _ := os.ReadFile(filepath.Join(w.Root, "SPEC.md"))
			edited := strings.Replace(string(data), "./custom/check*", "./custom/check", 1)
			if remove {
				edited = strings.Replace(edited, "skills:\n  - \"./custom/check\"\n", "skills: []\n", 1)
			}
			_ = os.WriteFile(filepath.Join(w.Root, "SPEC.md"), []byte(edited), 0o600)
			options, result, err := requestMergeEdits(&w, nil)
			if err != nil {
				t.Fatal(err)
			}
			if remove {
				if result[".telos/skills/check.json"] != nil || result["skills/check/SKILL.md"] != nil {
					t.Fatalf("removed skill restored: %+v", options)
				}
			} else if file := result[".telos/skills/check.json"]; file == nil || file.Content == nil || !strings.Contains(*file.Content, "false") {
				t.Fatalf("unstar reverted: %+v", options)
			}
		})
	}
}
func TestInitialMergeSyncResumesBeforeSavingPlan(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	workspace.Merge.Conflicts = nil
	workspace.SkillPaths = map[string]string{"check": "skills/check"}
	directory := filepath.Join(workspace.Root, "skills/check")
	_ = os.MkdirAll(directory, 0o755)
	_ = os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: check\n---\nOriginal skill.\n"), 0o600)
	workspace.Original, _ = workspace.snapshot()
	mergedSpec := strings.Replace(testPlanSpec, "Serve a demo.", "Serve my local proposal and deployed audit log.", 1)
	mergedSkill := "---\nname: check\n---\nKeep deployed skill change.\n"
	workspace.Merge.Files[0].Merged = mergeText(mergedSpec)
	workspace.Merge.Files = append(workspace.Merge.Files, cloud.RequestMergeFile{Path: "skills/check/SKILL.md", Merged: mergeText(mergedSkill)}, cloud.RequestMergeFile{Path: ".telos/skills/check.json", Kind: "skill_metadata", Merged: mergeText("{\"required\":false}\n")})
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID, request.CanEdit, request.PlanStale = 1, "cp_first", true, true
	request.CurrentRevisionID = workspace.Merge.CurrentRevisionID
	resolves := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/merge") {
			resolves++
			var options cloud.RequestMergeOptions
			_ = json.NewDecoder(r.Body).Decode(&options)
			for _, file := range options.Files {
				if file.Path == "skills/check/SKILL.md" && file.Content != nil && strings.Contains(*file.Content, "Original") {
					t.Error("partial sync discarded current skill edit")
				}
			}
			request.UpdateNumber, request.PreparedPlanID = 2, "cp_second"
			request.Preview.ProposedSpec = mergedSpec
		} else if r.Method != "GET" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	workspace.APIEndpoint = server.URL
	if err := workspace.save(); err != nil {
		t.Fatal(err)
	}
	// Simulate process exit after SPEC was written, before SKILL.md or phase advancement.
	local, err := workspace.localFiles(map[string]*cloud.MergeFile{"SPEC.md": mergeText(mergedSpec), ".telos/skills/check.json": mergeText("{\"required\":false}\n")})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(workspace.Root, "SPEC.md"), []byte(*local["SPEC.md"].Content), 0o600)
	captureStdout(t, func() {
		if err := runCloudRequestUpdate(filepath.Join(workspace.Root, "SPEC.md"), "cr_saved", "", "", "", true); err != nil {
			t.Fatal(err)
		}
	})
	skill, _ := os.ReadFile(filepath.Join(directory, "SKILL.md"))
	if string(skill) != mergedSkill || resolves != 1 {
		t.Fatalf("skill=%s resolves=%d", skill, resolves)
	}
}
func TestMergeSaveLostResponseReplaysExactBody(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	workspace.Merge.Conflicts = nil
	workspace.Merge.Files[0].Merged = mergeText(testPlanSpec)
	installTestMerge(t, &workspace)
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID, request.CanEdit, request.PlanStale = 1, "cp_first", true, true
	request.CurrentRevisionID = workspace.Merge.CurrentRevisionID
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/merge") {
			var options cloud.RequestMergeOptions
			_ = json.NewDecoder(r.Body).Decode(&options)
			encoded, _ := json.Marshal(options)
			bodies = append(bodies, string(encoded))
			request.UpdateNumber, request.PreparedPlanID, request.PlanStale = 2, "cp_second", false
			request.Preview.ProposedSpec = testPlanSpec
			if len(bodies) == 1 {
				http.Error(w, "response lost after commit", http.StatusInternalServerError)
				return
			}
		} else if r.Method != "GET" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	workspace.APIEndpoint = server.URL
	if err := workspace.save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace.Root, "SPEC.md")
	if err := runCloudRequestUpdate(path, "cr_saved", "", "first message", "", true); err == nil {
		t.Fatal("lost response succeeded")
	}
	captureStdout(t, func() {
		if err := runCloudRequestUpdate(path, "cr_saved", "", "changed invocation message", "", true); err != nil {
			t.Fatal(err)
		}
	})
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("unsafe retry changed request body: %v", bodies)
	}
}
func TestFileDirectoryConflictsStayUnchangedUntilResolved(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	workspace.Merge.Conflicts = nil
	workspace.SkillPaths = map[string]string{"check": "skills/check"}
	_ = os.MkdirAll(filepath.Join(workspace.Root, "skills/check"), 0o755)
	_ = os.WriteFile(filepath.Join(workspace.Root, "skills/check/docs"), []byte("local docs file"), 0o600)
	workspace.Original, _ = workspace.snapshot()
	files := map[string]*cloud.MergeFile{"SPEC.md": mergeText(testPlanSpec), ".telos/skills/check.json": mergeText("{\"required\":false}\n"), "skills/check/docs": mergeText("local docs file"), "skills/check/docs/README.md": mergeText("current directory docs")}
	if err := workspace.materialize(files, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(workspace.Root, "skills/check/docs"))
	if string(data) != "local docs file" {
		t.Fatal("initial conflict overwrote local file")
	}
	files["skills/check/docs"] = nil
	if err := workspace.materialize(files, false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(workspace.Root, "skills/check/docs/README.md"))
	if string(data) != "current directory docs" {
		t.Fatalf("file-to-directory resolution lost: %s", data)
	}
	files["skills/check/docs/README.md"] = nil
	files["skills/check/docs"] = mergeText("selected file")
	if err := workspace.materialize(files, false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(workspace.Root, "skills/check/docs"))
	if string(data) != "selected file" {
		t.Fatalf("directory-to-file resolution lost: %s", data)
	}
}

func TestRequestRecompileHonorsRemovedManifestSkills(t *testing.T) {
	directory := t.TempDir()
	skillDir := filepath.Join(directory, "skills/check")
	_ = os.MkdirAll(skillDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: check\n---\nCheck status.\n"), 0o600)
	specPath := filepath.Join(directory, "SPEC.md")
	original := strings.Replace(testPlanSpec, "skills: []", "skills: [\"./skills/check*\"]", 1)
	_ = os.WriteFile(specPath, []byte(original), 0o600)
	first, err := packageRequestSpec(specPath, "")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := spec.BuildApplyPackage(first.compiled)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(archive.Manifest)
	_ = os.WriteFile(filepath.Join(directory, "manifest.json"), manifest, 0o600)
	_ = os.WriteFile(specPath, []byte(strings.Replace(original, "check*", "check", 1)), 0o600)
	unstarred, err := packageRequestSpec(specPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.digest == unstarred.digest || len(unstarred.compiled.RequiredVerifierSkills) != 0 {
		t.Fatal("fresh compile did not detect changed requirement")
	}
	_ = os.WriteFile(specPath, []byte(testPlanSpec), 0o600)
	removed, err := packageRequestSpec(specPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.compiled.Skills) != 0 {
		t.Fatal("manifest resurrected removed skill")
	}
	data, _ := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if string(data) != string(manifest) {
		t.Fatal("generic user manifest was changed")
	}
}
func TestMergeStateAndFileTempsDoNotBlockRecoveryOrEnterPackage(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	_ = os.MkdirAll(filepath.Join(workspace.Root, ".telos"), 0o700)
	for _, name := range []string{filepath.Base(requestStatePath("cr_saved")) + ".tmp", ".request-state-interrupted", ".request-file-interrupted"} {
		_ = os.WriteFile(filepath.Join(workspace.Root, ".telos", name), []byte("interrupted bytes"), 0o600)
	}
	if err := workspace.save(); err != nil {
		t.Fatal(err)
	}
	files := map[string]*cloud.MergeFile{"SPEC.md": mergeText(testPlanSpec)}
	if err := workspace.materialize(files, true); err != nil {
		t.Fatal(err)
	}
	if err := workspace.save(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("internal temps entered package: %+v", snapshot)
	}
	info, _ := os.Stat(filepath.Join(workspace.Root, "SPEC.md"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private local permissions widened: %o", info.Mode().Perm())
	}
}
func TestResolutionDoesNotSilentlyDiscardRegistryReferenceChanges(t *testing.T) {
	content := strings.Replace(testPlanSpec, "skills: []", "skills: [\"@another/check:99.0.0\"]", 1)
	if _, err := localSpecRequirements(content, map[string]string{"check": "custom/check"}); err == nil || !strings.Contains(err.Error(), "local skill path") {
		t.Fatalf("Registry repin silently dropped: %v", err)
	}
}
func TestAutomaticPlanPreservesEditsMadeDuringUpload(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID, request.CanEdit, request.PlanStale = 1, "cp_first", true, true
	request.CurrentRevisionID = workspace.Merge.CurrentRevisionID
	path := filepath.Join(workspace.Root, "SPEC.md")
	edited := strings.Replace(testPlanSpec, "Serve a demo.", "New local edit while upload was in flight.", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/deployment-plans/packages" {
			_ = os.WriteFile(path, []byte(edited), 0o600)
		}
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method != "GET" || r.URL.Path != "/api/change-requests/cr_saved" {
			t.Errorf("unexpected merge save or prepare: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	control, _ := cloud.ControlClientForContext("")
	if err := recordRequestWorkspace(control, &request, path); err != nil {
		t.Fatal(err)
	}
	err := runCloudRequestUpdate(path, "cr_saved", "", "", "", true)
	if err == nil || !strings.Contains(err.Error(), "changed while uploading") {
		t.Fatalf("local race not detected: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != edited {
		t.Fatal("local edit overwritten")
	}
}
