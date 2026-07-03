package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

// fakeExecutor for testing local run.
type fakeExecutor struct {
	proverResult   game.TurnResult
	verifierResult game.TurnResult
	onExecute      func(role string)
	tasks          []string
}

func (f *fakeExecutor) ExecuteTurn(task string, role string, ts *game.TurnState) game.TurnResult {
	f.tasks = append(f.tasks, task)
	if f.onExecute != nil {
		f.onExecute(role)
	}
	if role == "prover" {
		return f.proverResult
	}
	return f.verifierResult
}

func (f *fakeExecutor) firstTask() string {
	if len(f.tasks) == 0 {
		return ""
	}
	return f.tasks[0]
}

func (f *fakeExecutor) taskAt(i int) string {
	if i < 0 || i >= len(f.tasks) {
		return ""
	}
	return f.tasks[i]
}

func (f *fakeExecutor) WorkspaceState() platform.WorkspaceSnapshot {
	return platform.WorkspaceSnapshot{Raw: "=== FILES ===\n(no files)", FileList: []string{"(no files)"}}
}

func (f *fakeExecutor) CheckpointWorkspace(dest string) bool {
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(dest, []byte("fake"), 0o644)
	return true
}

func writeTestSpec(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(dir, "telos-output"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: cli-test\nplatform: local\n---\n# CLI Test\n\nTest body."), 0o644)
	return specPath
}

func runTestCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func initTestGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, dir, "git", "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, dir, "git", "add", "-A")
	runTestCommand(t, dir, "git", "-c", "user.name=Telos", "-c", "user.email=telos@local", "commit", "-q", "-m", "initial")
	return strings.TrimSpace(runTestCommand(t, dir, "git", "rev-parse", "HEAD"))
}

func checkpointCompletedWorkspace(t *testing.T, session *LocalSession, specName string) string {
	t.Helper()
	workspacePath := filepath.Join(session.SessionDir, "specs", specName, "workspace.tar.gz")
	if ok := platform.NewLocalPlatform(filepath.Join(session.SessionDir, "workspace")).CheckpointWorkspace(workspacePath); !ok {
		t.Fatal("checkpoint workspace failed")
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := "2026-05-26T12:00:00.000Z"
	completed := "completed"
	manifest.Epochs = append(manifest.Epochs, sessionapi.Epoch{
		ID:         len(manifest.Epochs) + 1,
		StartedAt:  "2026-05-26T11:59:00.000Z",
		FinishedAt: &finishedAt,
		Result:     &completed,
	})
	if err := sessionapi.WriteManifest(filepath.Join(session.SessionDir, "session.json"), manifest); err != nil {
		t.Fatal(err)
	}
	return workspacePath
}

func TestCreateLocalSession(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	// Change to temp dir so the default session scope is stable.
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{
		Model:    "test-model",
		Thinking: "medium",
	})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	if session.SessionID == "" {
		t.Error("session ID should not be empty")
	}
	if session.SpecName != "cli-test" {
		t.Errorf("spec name: got %q", session.SpecName)
	}
	if !strings.HasPrefix(session.SessionID, "local_") {
		t.Errorf("session ID should start with local_: got %q", session.SessionID)
	}

	// Verify manifest exists
	manifestPath := filepath.Join(session.SessionDir, "session.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("manifest missing: %v", err)
	}

	// Verify manifest content
	data, _ := os.ReadFile(manifestPath)
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if m["session_id"] != session.SessionID {
		t.Errorf("manifest session_id: got %v", m["session_id"])
	}
	if m["spec_name"] != "cli-test" {
		t.Errorf("manifest spec_name: got %v", m["spec_name"])
	}
	if m["runtime"] != "local" {
		t.Errorf("manifest runtime: got %v", m["runtime"])
	}
	cfg, _ := m["config"].(map[string]interface{})
	if cfg["model"] != "test-model" {
		t.Errorf("manifest model: got %v", cfg["model"])
	}

	// Verify spec was copied
	specDir := filepath.Join(session.SessionDir, "specs", "cli-test")
	if _, err := os.Stat(filepath.Join(specDir, "spec.md")); err != nil {
		t.Errorf("spec not copied: %v", err)
	}
}

func TestCreateLocalSessionUsesManagedProfileDefaultModel(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)
	t.Setenv("TELOS_GATEWAY_MODE", "managed")

	session, err := CreateLocalSession(specPath, LocalRunConfig{ModelProfile: sessionapi.ModelProfilePremium})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Config.ModelProfile != sessionapi.ModelProfilePremium {
		t.Fatalf("model profile: got %q", manifest.Config.ModelProfile)
	}
	if manifest.Config.Model != "telos-bifrost/premium-agent" {
		t.Fatalf("model: got %q", manifest.Config.Model)
	}
}

func TestCreateLocalSessionRecordsParentSession(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)
	parentID := "local_parent"

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{ParentSessionID: &parentID})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ParentSessionID == nil || *manifest.ParentSessionID != parentID {
		t.Fatalf("parent session id: got %#v, want %q", manifest.ParentSessionID, parentID)
	}
}

func TestEnsureSessionWorkspaceInitializesAPIBackedSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: cloud-fresh\nplatform: cloud\n---\n# Cloud Fresh\n\nRun something."

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.SessionDir == nil {
		t.Fatal("missing session dir")
	}
	active := filepath.Join(*session.SessionDir, "workspace")
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("fresh API session should not pre-create workspace: %v", err)
	}

	manifestPath := filepath.Join(*session.SessionDir, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSessionWorkspace(*session.SessionDir, manifest); err != nil {
		t.Fatalf("ensureSessionWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(active, ".git")); err != nil {
		t.Fatalf("workspace should be initialized as a git repo: %v", err)
	}
	updated, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Workspace == nil || updated.Workspace.Mode != workspaceModeEmpty {
		t.Fatalf("workspace metadata: %#v", updated.Workspace)
	}
	if updated.Workspace.BaseCommit == "" {
		t.Fatal("workspace metadata should record base commit")
	}
}

func TestEnsureSessionWorkspaceRefusesSilentDowngrade(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, filepath.Join(dir, "spec"))
	source := filepath.Join(dir, "source")
	initTestGitRepo(t, source)

	session, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: source})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(session.SessionDir, "workspace")); err != nil {
		t.Fatal(err)
	}

	manifestPathStr := filepath.Join(session.SessionDir, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPathStr)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Workspace == nil || manifest.Workspace.Mode != "git_clone" {
		t.Fatalf("precondition: workspace mode should be git_clone, got %#v", manifest.Workspace)
	}

	err = ensureSessionWorkspace(session.SessionDir, manifest)
	if err == nil {
		t.Fatal("expected ensureSessionWorkspace to refuse silent downgrade")
	}
	if !strings.Contains(err.Error(), "git_clone") {
		t.Fatalf("error should mention the original mode: %v", err)
	}

	updated, err := sessionapi.ReadManifest(manifestPathStr)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Workspace == nil || updated.Workspace.Mode != "git_clone" {
		t.Fatalf("manifest workspace must not be downgraded: %#v", updated.Workspace)
	}
}

func TestCreateLocalSessionRejectsWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)
	workspacePath := filepath.Join(dir, "workspace-file")
	if err := os.WriteFile(workspacePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: workspacePath})
	if err == nil {
		t.Fatal("expected invalid workspace error")
	}
	if !strings.Contains(err.Error(), "workspace path must be a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateLocalSessionHonorsSessionDirEnv(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionsRoot := filepath.Join(dir, "telos-sessions")
	t.Setenv("TELOS_SESSION_DIR", sessionsRoot)

	session, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: workspace})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	if !strings.HasPrefix(session.SessionDir, sessionsRoot+string(os.PathSeparator)) {
		t.Fatalf("session dir %q should be under %q", session.SessionDir, sessionsRoot)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".telos")); !os.IsNotExist(err) {
		t.Fatalf("workspace should not contain .telos when TELOS_SESSION_DIR is set: %v", err)
	}
}

func TestCreateLocalSessionClonesCleanGitWorkspace(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, filepath.Join(dir, "spec"))
	source := filepath.Join(dir, "source")
	base := initTestGitRepo(t, source)
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}

	session, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: source})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	active := filepath.Join(session.SessionDir, "workspace")
	if active == source {
		t.Fatal("session workspace should not be the source checkout")
	}
	if _, err := os.Stat(filepath.Join(active, ".git")); err != nil {
		t.Fatalf("cloned workspace missing .git: %v", err)
	}
	if got := strings.TrimSpace(runTestCommand(t, active, "git", "rev-parse", "HEAD")); got != base {
		t.Fatalf("base checkout: got %s want %s", got, base)
	}
	if got := strings.TrimSpace(runTestCommand(t, active, "git", "remote")); got != "" {
		t.Fatalf("session workspace should not keep remotes: %q", got)
	}
	marker := filepath.Join(source, ".telos")
	info, err := os.Lstat(marker)
	if err != nil {
		t.Fatalf("source workspace missing .telos marker: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal(".telos marker should be a symlink")
	}
	target, err := os.Readlink(marker)
	if err != nil {
		t.Fatalf("read .telos marker: %v", err)
	}
	if !samePath(resolveLinkTarget(source, target), filepath.Dir(filepath.Dir(session.SessionDir))) {
		t.Fatalf(".telos target: got %q want %q", target, filepath.Dir(filepath.Dir(session.SessionDir)))
	}

	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Workspace == nil {
		t.Fatal("manifest missing workspace metadata")
	}
	if manifest.Workspace.Mode != "git_clone" {
		t.Fatalf("workspace mode: got %q", manifest.Workspace.Mode)
	}
	if manifest.Workspace.Source != source {
		t.Fatalf("workspace source: got %q want %q", manifest.Workspace.Source, source)
	}
	if manifest.Workspace.BaseCommit != base {
		t.Fatalf("base commit: got %q want %q", manifest.Workspace.BaseCommit, base)
	}
	if _, ok := manifest.Config.AsMap()["workspace"]; ok {
		t.Fatal("config should not persist the live workspace path")
	}

	if _, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: source}); err != nil {
		t.Fatalf("second CreateLocalSession should ignore .telos marker dirtiness: %v", err)
	}
}

func TestCreateLocalSessionDefaultsToCwdGitRoot(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "repo")
	base := initTestGitRepo(t, source)
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(source, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: cwd-default\nplatform: local\n---\n# Cwd Default\n\nTest body."), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, source, "git", "add", "SPEC.md")
	runTestCommand(t, source, "git", "-c", "user.name=Telos", "-c", "user.email=telos@local", "commit", "-q", "-m", "add spec")
	base = strings.TrimSpace(runTestCommand(t, source, "git", "rev-parse", "HEAD"))
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(dir, "telos-output"))

	orig, _ := os.Getwd()
	os.Chdir(source)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Workspace == nil {
		t.Fatal("manifest missing workspace metadata")
	}
	if manifest.Workspace.Mode != "git_clone" {
		t.Fatalf("workspace mode: got %q, want git_clone", manifest.Workspace.Mode)
	}
	if manifest.Workspace.Source != source {
		t.Fatalf("workspace source: got %q, want %q", manifest.Workspace.Source, source)
	}
	if manifest.Workspace.BaseCommit != base {
		t.Fatalf("base commit: got %q, want %q", manifest.Workspace.BaseCommit, base)
	}
	active := filepath.Join(session.SessionDir, "workspace")
	if active == source {
		t.Fatal("session workspace should not be the source checkout")
	}
	if _, err := os.Stat(filepath.Join(active, ".git")); err != nil {
		t.Fatalf("cloned workspace missing .git: %v", err)
	}
}

func TestCreateLocalSessionDefaultsToCwdWhenNotInGitRepo(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Workspace == nil || manifest.Workspace.Mode != workspaceModeEmpty {
		t.Fatalf("workspace metadata: %#v", manifest.Workspace)
	}
}

func TestCreateLocalSessionRejectsDirtyGitWorkspace(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, filepath.Join(dir, "spec"))
	source := filepath.Join(dir, "source")
	initTestGitRepo(t, source)
	if err := os.WriteFile(filepath.Join(source, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: source})
	if err == nil {
		t.Fatal("expected dirty workspace error")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateLocalSessionRejectsGitLFSWorkspace(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, filepath.Join(dir, "spec"))
	source := filepath.Join(dir, "source")
	initTestGitRepo(t, source)
	if err := os.WriteFile(filepath.Join(source, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, source, "git", "add", ".gitattributes")
	runTestCommand(t, source, "git", "-c", "user.name=Telos", "-c", "user.email=telos@local", "commit", "-q", "-m", "mark lfs")

	_, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: source})
	if err == nil {
		t.Fatal("expected git-lfs workspace error")
	}
	if !strings.Contains(err.Error(), "git-lfs") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateLocalSessionSnapshotsPlainDirectory(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, filepath.Join(dir, "spec"))
	source := filepath.Join(dir, "plain")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	session, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: source})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	active := filepath.Join(session.SessionDir, "workspace")
	if _, err := os.Stat(filepath.Join(active, ".git")); err != nil {
		t.Fatalf("snapshot workspace missing .git: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(active, "notes.txt")); err != nil || string(data) != "snapshot\n" {
		t.Fatalf("snapshot file: data=%q err=%v", data, err)
	}

	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Workspace == nil || manifest.Workspace.Mode != "snapshot" {
		t.Fatalf("workspace metadata: %#v", manifest.Workspace)
	}
	if manifest.Workspace.BaseCommit == "" {
		t.Fatal("snapshot should record base_commit")
	}
}

func TestCreateLocalSessionExtendsCompletedWorkspaceArtifact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(dir, "telos-output"))

	baseSpec := filepath.Join(dir, "base", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(baseSpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseSpec, []byte("---\nversion: v0\nname: base-spec\nplatform: local\n---\nBase body"), 0o644); err != nil {
		t.Fatal(err)
	}
	childSpec := filepath.Join(dir, "child", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(childSpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childSpec, []byte("---\nversion: v0\nname: child-spec\nplatform: local\nextends: ../base/SPEC.md\n---\nChild body"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	baseSession, err := CreateLocalSession(baseSpec, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseSession.SessionDir, "workspace", "base.txt"), []byte("parent artifact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseWorkspacePath := checkpointCompletedWorkspace(t, baseSession, "base-spec")

	childSession, err := CreateLocalSession(childSpec, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession child: %v", err)
	}
	childData, err := os.ReadFile(filepath.Join(childSession.SessionDir, "workspace", "base.txt"))
	if err != nil {
		t.Fatalf("child workspace should inherit parent artifact file: %v", err)
	}
	if string(childData) != "parent artifact\n" {
		t.Fatalf("child artifact file: got %q", childData)
	}

	manifest, err := sessionapi.ReadManifest(filepath.Join(childSession.SessionDir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Workspace == nil || manifest.Workspace.Mode != "artifact" {
		t.Fatalf("workspace metadata: %#v", manifest.Workspace)
	}
	if manifest.Workspace.Extends == nil {
		t.Fatal("workspace should record extended artifact binding")
	}
	if manifest.Workspace.Extends.SessionID != baseSession.SessionID {
		t.Fatalf("extends session: got %q want %q", manifest.Workspace.Extends.SessionID, baseSession.SessionID)
	}
	if manifest.Workspace.Extends.WorkspacePath != baseWorkspacePath {
		t.Fatalf("extends workspace path: got %q want %q", manifest.Workspace.Extends.WorkspacePath, baseWorkspacePath)
	}
	if manifest.Workspace.Extends.ContentHash == "" {
		t.Fatal("extends binding should record content hash")
	}
}

func TestCreateLocalSessionExtendsRequiresCompletedWorkspaceArtifact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(dir, "telos-output"))

	baseSpec := filepath.Join(dir, "base", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(baseSpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseSpec, []byte("---\nversion: v0\nname: base-spec\nplatform: local\n---\nBase body"), 0o644); err != nil {
		t.Fatal(err)
	}
	childSpec := filepath.Join(dir, "child", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(childSpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childSpec, []byte("---\nversion: v0\nname: child-spec\nplatform: local\nextends: ../base/SPEC.md\n---\nChild body"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	_, err := CreateLocalSession(childSpec, LocalRunConfig{})
	if err == nil {
		t.Fatal("expected missing parent artifact error")
	}
	if !strings.Contains(err.Error(), "run the parent spec first") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateLocalSessionExtendsActiveLocalControllerWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(dir, "telos-output"))

	baseSpec := filepath.Join(dir, "base", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(baseSpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseSpec, []byte("---\nversion: v0\nname: base-spec\nplatform: local\n---\nBase body"), 0o644); err != nil {
		t.Fatal(err)
	}
	childSpec := filepath.Join(dir, "base", "generated", "child", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(childSpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childSpec, []byte("---\nversion: v0\nname: child-spec\nplatform: local\nextends: ../../SPEC.md\n---\nChild body"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	baseSession, err := CreateLocalSession(baseSpec, LocalRunConfig{SessionKind: sessionapi.KindController})
	if err != nil {
		t.Fatalf("CreateLocalSession base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseSession.SessionDir, "workspace", "controller.txt"), []byte("active controller workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessionsRoot := filepath.Dir(baseSession.SessionDir)
	t.Setenv("TELOS_SESSION_DIR", sessionsRoot)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))
	t.Setenv("TELOS_SESSION_ID", baseSession.SessionID)

	childSession, err := CreateLocalSession(childSpec, LocalRunConfig{ParentSessionID: &baseSession.SessionID})
	if err != nil {
		t.Fatalf("CreateLocalSession child: %v", err)
	}
	childData, err := os.ReadFile(filepath.Join(childSession.SessionDir, "workspace", "controller.txt"))
	if err != nil {
		t.Fatalf("child workspace should inherit active controller workspace: %v", err)
	}
	if string(childData) != "active controller workspace\n" {
		t.Fatalf("child active workspace file: got %q", childData)
	}

	manifest, err := sessionapi.ReadManifest(filepath.Join(childSession.SessionDir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Workspace == nil || manifest.Workspace.Extends == nil {
		t.Fatalf("workspace should record active parent binding: %#v", manifest.Workspace)
	}
	if manifest.Workspace.Extends.SessionID != baseSession.SessionID {
		t.Fatalf("extends session: got %q want %q", manifest.Workspace.Extends.SessionID, baseSession.SessionID)
	}
	if manifest.Workspace.Extends.WorkspacePath != filepath.Join(baseSession.SessionDir, "workspace") {
		t.Fatalf("extends workspace path: got %q", manifest.Workspace.Extends.WorkspacePath)
	}
}

func TestRunLocalSessionWithFakeExecutor(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "I built the thing.\n\n<progress_update>Built</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "Looks good.\n\n<status>CONCEDE</status>\n",
		},
	}

	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}

	if result.GameResult != game.GameSuccess {
		t.Errorf("game result: got %s", result.GameResult)
	}
	if result.VerifierConceded != true {
		t.Error("expected verifier conceded")
	}

	// Verify epoch was written
	manifestData, _ := os.ReadFile(filepath.Join(session.SessionDir, "session.json"))
	var m map[string]interface{}
	json.Unmarshal(manifestData, &m)
	epochs, _ := m["epochs"].([]interface{})
	if len(epochs) == 0 {
		t.Fatal("expected at least 1 epoch")
	}
	epoch0, _ := epochs[0].(map[string]interface{})
	if epoch0["result"] != "completed" {
		t.Errorf("epoch result: got %v", epoch0["result"])
	}

	// Verify session can be read by the store
	store := sessionapi.NewFileStore(filepath.Dir(session.SessionDir), sessionapi.RuntimeLocal)
	sessionAPI, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if sessionAPI.Status != sessionapi.StatusCompleted {
		t.Errorf("status: got %s", sessionAPI.Status)
	}
}

func TestCreateLocalSessionPersistsUntil(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{Until: 2})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	manifest, err := sessionapi.ReadManifest(filepath.Join(session.SessionDir, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Config.Until != 2 {
		t.Fatalf("until: got %d", manifest.Config.Until)
	}
}

func TestLocalWorkerEnvIncludesSessionContext(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)
	parentID := "local_parent"
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)
	session, err := CreateLocalSession(specPath, LocalRunConfig{ParentSessionID: &parentID})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	values := map[string]string{}
	for _, entry := range sessionworker.Env(session.SessionDir, sessionworker.StartOptions{Runtime: sessionapi.RuntimeLocal}) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	if values["TELOS_SESSION_DIR"] != filepath.Dir(session.SessionDir) {
		t.Fatalf("TELOS_SESSION_DIR: got %q", values["TELOS_SESSION_DIR"])
	}
	if values["TELOS_SESSION_ID"] != session.SessionID {
		t.Fatalf("TELOS_SESSION_ID: got %q, want %q", values["TELOS_SESSION_ID"], session.SessionID)
	}
	if values["TELOS_RUNTIME"] != string(sessionapi.RuntimeLocal) {
		t.Fatalf("TELOS_RUNTIME: got %q, want %q", values["TELOS_RUNTIME"], sessionapi.RuntimeLocal)
	}
	if values["TELOS_PARENT_SESSION_ID"] != parentID {
		t.Fatalf("TELOS_PARENT_SESSION_ID: got %q, want %q", values["TELOS_PARENT_SESSION_ID"], parentID)
	}
}

func TestRunLocalControllerSessionUsesControllerPrompt(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	manifestPath := filepath.Join(session.SessionDir, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SessionKind = sessionapi.KindController
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Observed controller state.\n\n<progress_update>Observed</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "OK\n\n<status>CONCEDE</status>\n",
		},
	}
	if _, err := RunLocalSessionWithExecutor(session.SessionDir, exec); err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}

	task := exec.firstTask()
	if !strings.Contains(task, "## Controller Role") {
		t.Fatal("controller session should receive controller prompt")
	}
	if !strings.Contains(task, "`telos-orchestrate`") {
		t.Fatal("controller prompt should include telos-orchestrate skill")
	}
	if !strings.Contains(task, "Primary spec: `") {
		t.Fatal("controller prompt should include primary spec path")
	}
}

func TestRunLocalSessionPromptsReadTranscriptFirst(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	store := sessionapi.NewFileStore(filepath.Dir(session.SessionDir), sessionapi.RuntimeLocal)
	proverTurns := 0

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Implemented something.\n\n<progress_update>Implemented</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusContinue,
			Logs:   "Finding remains.\n\n<status>CONTINUE</status>\n",
		},
		onExecute: func(role string) {
			if role != "prover" {
				return
			}
			proverTurns++
			if proverTurns == 2 {
				if _, err := store.Stop(session.SessionID); err != nil {
					t.Fatalf("Stop: %v", err)
				}
			}
		},
	}
	if _, err := RunLocalSessionWithExecutor(session.SessionDir, exec); err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}

	firstImplementationTask := exec.taskAt(0)
	if firstImplementationTask == "" {
		t.Fatalf("expected first implementation task, got %d tasks", len(exec.tasks))
	}
	if !strings.Contains(firstImplementationTask, "Start from the Current State Digest above") {
		t.Fatal("first implementation prompt should include digest-first transcript guidance")
	}

	evaluationTask := exec.taskAt(1)
	if evaluationTask == "" {
		t.Fatalf("expected evaluation task, got %d tasks", len(exec.tasks))
	}
	if !strings.Contains(evaluationTask, "Start from the Current State Digest above") {
		t.Fatal("evaluation prompt should include digest-first transcript guidance")
	}

	secondImplementationTask := exec.taskAt(2)
	if secondImplementationTask == "" {
		t.Fatalf("expected second implementation task, got %d tasks", len(exec.tasks))
	}
	if !strings.Contains(secondImplementationTask, "Start from the Current State Digest above") {
		t.Fatal("second implementation prompt should include digest-first transcript guidance")
	}
	if !strings.Contains(secondImplementationTask, "identify unresolved evaluator findings") {
		t.Fatal("implementation prompt should identify unresolved evaluator findings")
	}
}

func TestRunLocalSessionRemovesWorkspaceAfterCheckpoint(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Built.\n\n<progress_update>done</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "LGTM\n\n<status>CONCEDE</status>\n",
		},
	}
	if _, err := RunLocalSessionWithExecutor(session.SessionDir, exec); err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}
	if _, err := os.Stat(filepath.Join(session.SessionDir, "workspace")); !os.IsNotExist(err) {
		t.Fatalf("default workspace should be removed after checkpoint: %v", err)
	}
}

func TestRunLocalSessionRehydratesWorkspaceFromCheckpoint(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	if err := os.WriteFile(filepath.Join(session.SessionDir, "workspace", "state.txt"), []byte("checkpoint\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpointCompletedWorkspace(t, session, "cli-test")
	if err := os.RemoveAll(filepath.Join(session.SessionDir, "workspace")); err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Built.\n\n<progress_update>done</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "LGTM\n\n<status>CONCEDE</status>\n",
		},
		onExecute: func(role string) {
			if role != "prover" {
				return
			}
			if data, err := os.ReadFile(filepath.Join(session.SessionDir, "workspace", "state.txt")); err != nil || string(data) != "checkpoint\n" {
				t.Fatalf("rehydrated state: data=%q err=%v", data, err)
			}
		},
	}
	if _, err := RunLocalSessionWithExecutor(session.SessionDir, exec); err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}
}

func TestRunLocalSessionResolvesRelativeSkillsAgainstOriginalSpecDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(dir, "telos-output"))

	srcDir := filepath.Join(dir, "src")
	skillDir := filepath.Join(srcDir, "skills", "runner-rel-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: runner-rel-skill\ndescription: Relative\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(srcDir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: runner-rel\nplatform: local\nskills:\n  - skills/runner-rel-skill\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "ok\n\n<progress_update>ok</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "ok\n\n<status>CONCEDE</status>\n",
		},
	}
	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}
	if result.GameResult != game.GameSuccess {
		t.Fatalf("game result: got %s", result.GameResult)
	}
	if !strings.Contains(exec.firstTask(), "`runner-rel-skill`") {
		t.Fatal("prompt should include relative skill resolved against original spec dir")
	}
}

func TestRunLocalSessionRejectsMissingSessionSpecPath(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	manifestPath := filepath.Join(session.SessionDir, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Specs[0].SessionSpecPath = nil
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	_, err = RunLocalSessionWithExecutor(session.SessionDir, &fakeExecutor{})
	if err == nil {
		t.Fatal("expected missing session_spec_path error")
	}
	if !strings.Contains(err.Error(), "session_spec_path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunLocalSessionStopsWhenManifestIsStopped(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	store := sessionapi.NewFileStore(filepath.Dir(session.SessionDir), sessionapi.RuntimeLocal)

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "started",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusContinue,
			Logs:   "should not run",
		},
		onExecute: func(role string) {
			if role != "prover" {
				return
			}
			if _, err := store.Stop(session.SessionID); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		},
	}

	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}
	if result.GameResult != game.GameStopped {
		t.Fatalf("game result: got %s", result.GameResult)
	}

	sessionAPI, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if sessionAPI.Status != sessionapi.StatusStopped {
		t.Fatalf("status: got %s", sessionAPI.Status)
	}
}

func TestEndToEndSmokeTest(t *testing.T) {
	// run -> describe -> logs -> list -> stop
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Built.\n\n<progress_update>done</progress_update>",
			Stats:  game.TurnStats{CostUSD: 0.10, InputTokens: 500, OutputTokens: 200},
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "LGTM\n\n<status>CONCEDE</status>\n",
			Stats:  game.TurnStats{CostUSD: 0.05, InputTokens: 300, OutputTokens: 100},
		},
	}

	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.GameResult != game.GameSuccess {
		t.Fatalf("game: got %s", result.GameResult)
	}

	store := sessionapi.NewFileStore(filepath.Dir(session.SessionDir), sessionapi.RuntimeLocal)

	// describe
	sess, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if sess.Status != sessionapi.StatusCompleted {
		t.Errorf("describe status: got %s", sess.Status)
	}
	if sess.SpecName == nil || *sess.SpecName != "cli-test" {
		t.Errorf("describe spec_name: got %v", sess.SpecName)
	}

	// logs
	transcript, err := store.Transcript(session.SessionID)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if !strings.Contains(transcript, "Implementation 1") {
		t.Error("transcript should contain implementation turn")
	}
	if !strings.Contains(transcript, "Evaluation 1") {
		t.Error("transcript should contain evaluation turn")
	}

	// events
	events, err := store.Events(session.SessionID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected evidence events")
	}
	hasGameStart := false
	hasGameEnd := false
	for _, ev := range events {
		if ev.Event == "game_start" {
			hasGameStart = true
		}
		if ev.Event == "game_end" {
			hasGameEnd = true
		}
	}
	if !hasGameStart {
		t.Error("missing game_start event")
	}
	if !hasGameEnd {
		t.Error("missing game_end event")
	}

	// list
	sessions, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].SessionID != session.SessionID {
		t.Errorf("list session ID: got %q", sessions[0].SessionID)
	}

	// stop (already completed, should be idempotent)
	stopped, err := store.Stop(session.SessionID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Status != sessionapi.StatusCompleted {
		t.Errorf("stop on completed: got %s", stopped.Status)
	}
}

func TestSessionArtifactShape(t *testing.T) {
	// Verify the on-disk artifact shape matches Python expectations
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, _ := CreateLocalSession(specPath, LocalRunConfig{})

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "done\n\n<progress_update>done</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "ok\n\n<status>CONCEDE</status>\n",
		},
	}

	RunLocalSessionWithExecutor(session.SessionDir, exec)

	// Check expected files exist
	expected := []string{
		"session.json",
		filepath.Join("specs", "cli-test", "evidence.jsonl"),
		filepath.Join("specs", "cli-test", "transcript-"+session.SessionID+".md"),
		filepath.Join("specs", "cli-test", "spec.md"),
		filepath.Join("specs", "cli-test", "workspace.tar.gz"),
		filepath.Join("specs", "cli-test", "turns", "0001-prover", "task.md"),
		filepath.Join("specs", "cli-test", "turns", "0002-verifier", "task.md"),
	}

	for _, rel := range expected {
		path := filepath.Join(session.SessionDir, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing artifact: %s", rel)
		}
	}

	// Verify session.json has Python-compatible shape
	data, _ := os.ReadFile(filepath.Join(session.SessionDir, "session.json"))
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	requiredKeys := []string{
		"session_id", "session_kind", "created_at", "launcher",
		"source_spec_path", "session_spec_path", "spec_name",
		"config", "provenance", "specs", "epochs",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("manifest missing key %q", key)
		}
	}

	specs, _ := m["specs"].([]interface{})
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	spec0, _ := specs[0].(map[string]interface{})
	specKeys := []string{"index", "name", "dir_name", "evidence_path", "transcript_path", "workspace_path"}
	for _, key := range specKeys {
		if _, ok := spec0[key]; !ok {
			t.Errorf("spec missing key %q", key)
		}
	}
}

func TestRunnerIdentityRecordsKubernetesPod(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("HOSTNAME", "controller-abc")
	t.Setenv("TELOS_RUNNER_POD_NAME", "controller-abc")
	t.Setenv("TELOS_RUNNER_POD_NAMESPACE", "ns-ctrl-abc")

	runner := sessionworker.RunnerIdentity(1234)

	if runner.Kind != "kubernetes-pod" {
		t.Fatalf("kind: got %v", runner.Kind)
	}
	if !runner.InCluster {
		t.Fatalf("in_cluster: got %v", runner.InCluster)
	}
	if runner.PodName != "controller-abc" {
		t.Fatalf("pod_name: got %v", runner.PodName)
	}
	if runner.PodNamespace != "ns-ctrl-abc" {
		t.Fatalf("pod_namespace: got %v", runner.PodNamespace)
	}
}
