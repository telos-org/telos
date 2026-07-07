package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

// pathExists reports whether path is present. Returns (false, err) for any
// stat error other than ErrNotExist so callers can distinguish "missing"
// from "I/O or permission failure" instead of collapsing both into a
// silent fallback.
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

const (
	outputRootEnv = "TELOS_OUTPUT_ROOT"

	workspaceModeEmpty    = "empty"
	workspaceModeArtifact = "artifact"
	workspaceModeGitClone = "git_clone"
	workspaceModeSnapshot = "snapshot"

	sessionGitUserEmail = "telos@local"
	sessionGitUserName  = "Telos"
)

func workspaceScope(requested string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("resolve current workspace scope: %w", err)
		}
		abs, err := canonicalPath(cwd)
		if err != nil {
			return "", "", fmt.Errorf("resolve current workspace scope: %w", err)
		}
		if root, ok := gitTopLevel(abs); ok {
			return root, root, nil
		}
		return "", abs, nil
	}

	source, err := filepath.Abs(requested)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace path: %w", err)
	}
	source, err = canonicalPath(source)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", "", fmt.Errorf("read workspace path: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("workspace path must be a directory: %s", source)
	}
	if root, ok := gitTopLevel(source); ok {
		source = root
	}
	return source, source, nil
}

func DefaultLocalSessionRoot(scope string) (string, error) {
	if root := strings.TrimSpace(os.Getenv("TELOS_SESSION_DIR")); root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve sessions root: %w", err)
		}
		return abs, nil
	}
	if strings.TrimSpace(scope) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current workspace scope: %w", err)
		}
		scope = cwd
	}
	absScope, err := filepath.Abs(scope)
	if err != nil {
		return "", fmt.Errorf("resolve workspace scope: %w", err)
	}
	absScope, err = canonicalPath(absScope)
	if err != nil {
		return "", fmt.Errorf("resolve workspace scope: %w", err)
	}
	if markerRoot, ok := existingScopeMarker(absScope); ok {
		return filepath.Join(markerRoot, "sessions"), nil
	}
	outputRoot, err := LocalOutputRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(outputRoot, "execroot", workspaceID(absScope), "sessions"), nil
}

func LocalOutputRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv(outputRootEnv)); root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", outputRootEnv, err)
		}
		return abs, nil
	}

	if runtime.GOOS != "darwin" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" {
			return filepath.Join(xdg, "telos"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "telos"), nil
	}
	return filepath.Join(home, ".cache", "telos"), nil
}

func prepareSessionWorkspace(sessionDir string, source string, compiled *spec.CompiledEnvironment, sessionsRoot string) (*sessionapi.Workspace, error) {
	active := activeWorkspacePath(sessionDir)
	if err := os.RemoveAll(active); err != nil {
		return nil, fmt.Errorf("reset session workspace: %w", err)
	}

	if compiled != nil && compiled.ExtendsCompiled != nil {
		return prepareArtifactWorkspace(active, source, compiled.ExtendsCompiled, sessionsRoot)
	}

	if strings.TrimSpace(source) == "" {
		base, err := initSnapshotRepo(active)
		if err != nil {
			return nil, err
		}
		return &sessionapi.Workspace{
			Mode:       workspaceModeEmpty,
			BaseCommit: base,
		}, nil
	}

	if isGitRepo(source) {
		return cloneGitWorkspace(source, active)
	}

	if err := copySnapshot(source, active); err != nil {
		return nil, err
	}
	base, err := initSnapshotRepo(active)
	if err != nil {
		return nil, err
	}
	return &sessionapi.Workspace{
		Mode:       workspaceModeSnapshot,
		Source:     source,
		BaseCommit: base,
	}, nil
}

func activeWorkspacePath(sessionDir string) string {
	return filepath.Join(sessionDir, "workspace")
}

func ensureSessionWorkspace(sessionDir string, manifest *sessionapi.Manifest) error {
	active := activeWorkspacePath(sessionDir)
	if _, err := os.Stat(active); err == nil {
		if err := configureSessionGitIdentity(active); err != nil {
			return fmt.Errorf("configure session workspace git identity: %w", err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect session workspace: %w", err)
	}

	archive, err := latestWorkspaceCheckpoint(manifest)
	if err != nil {
		return err
	}
	if archive == "" {
		// Only initialize an empty workspace if the manifest never recorded a
		// source workspace (fresh API-backed session) or already declared the
		// empty mode. Otherwise the recorded mode (git_clone, snapshot,
		// artifact) requires real source state and we must not silently
		// downgrade to an empty repo — the original source is gone and the
		// runner has no way to fetch it back.
		if manifest.Workspace != nil && manifest.Workspace.Mode != "" && manifest.Workspace.Mode != workspaceModeEmpty {
			return fmt.Errorf("session workspace (%s) is missing and no saved checkpoint exists to restore it; recreate the session", manifest.Workspace.Mode)
		}
		base, err := initSnapshotRepo(active)
		if err != nil {
			return fmt.Errorf("initialize session workspace: %w", err)
		}
		manifest.Workspace = &sessionapi.Workspace{
			Mode:       workspaceModeEmpty,
			BaseCommit: base,
		}
		if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
			return fmt.Errorf("record session workspace: %w", err)
		}
		return nil
	}
	if err := extractWorkspaceArtifact(archive, active); err != nil {
		return fmt.Errorf("restore session workspace: %w", err)
	}
	if err := configureSessionGitIdentity(active); err != nil {
		return fmt.Errorf("configure restored workspace git identity: %w", err)
	}
	return nil
}

func latestWorkspaceCheckpoint(manifest *sessionapi.Manifest) (string, error) {
	if manifest == nil {
		return "", nil
	}
	for i := len(manifest.Specs) - 1; i >= 0; i-- {
		spec := manifest.Specs[i]
		if spec.WorkspacePath == nil || *spec.WorkspacePath == "" {
			continue
		}
		ok, err := pathExists(*spec.WorkspacePath)
		if err != nil {
			return "", fmt.Errorf("inspect workspace checkpoint %s: %w", *spec.WorkspacePath, err)
		}
		if ok {
			return *spec.WorkspacePath, nil
		}
	}
	return "", nil
}

func cleanupSessionWorkspace(sessionDir string, resultPath string) error {
	if resultPath == "" {
		return nil
	}
	ok, err := pathExists(resultPath)
	if err != nil {
		return fmt.Errorf("inspect workspace result %s: %w", resultPath, err)
	}
	if !ok {
		return nil
	}
	return os.RemoveAll(activeWorkspacePath(sessionDir))
}

func prepareArtifactWorkspace(dest string, source string, parent *spec.CompiledEnvironment, sessionsRoot string) (*sessionapi.Workspace, error) {
	binding, err := resolveExtendedWorkspaceArtifact(sessionsRoot, parent)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(binding.WorkspacePath)
	if err != nil {
		return nil, fmt.Errorf("inspect extended workspace artifact: %w", err)
	}
	if info.IsDir() {
		if err := copySnapshot(binding.WorkspacePath, dest); err != nil {
			return nil, fmt.Errorf("copy extended workspace: %w", err)
		}
	} else if err := extractWorkspaceArtifact(binding.WorkspacePath, dest); err != nil {
		return nil, fmt.Errorf("extract extended workspace artifact: %w", err)
	}
	base, err := workspaceHeadCommit(dest)
	if err != nil {
		return nil, err
	}
	return &sessionapi.Workspace{
		Mode:       workspaceModeArtifact,
		Source:     source,
		BaseCommit: base,
		Extends:    binding,
	}, nil
}

func cloneGitWorkspace(source string, dest string) (*sessionapi.Workspace, error) {
	if dirty, err := gitDirtyStatus(source); err != nil {
		return nil, fmt.Errorf("inspect workspace status: %w", err)
	} else if strings.TrimSpace(dirty) != "" {
		return nil, fmt.Errorf("workspace has uncommitted changes; commit or stash changes before launching a local Telos run")
	}
	if ok, err := pathExists(filepath.Join(source, ".gitmodules")); err != nil {
		return nil, fmt.Errorf("inspect workspace submodules: %w", err)
	} else if ok {
		return nil, fmt.Errorf("workspace uses git submodules; local isolated workspaces do not support submodules yet")
	}
	if usesLFS, err := workspaceUsesLFS(source); err != nil {
		return nil, err
	} else if usesLFS {
		return nil, fmt.Errorf("workspace uses git-lfs; local isolated workspaces do not support lfs yet")
	}

	base, err := gitOutput(source, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve workspace base commit: %w", err)
	}
	base = strings.TrimSpace(base)

	if err := runCommand("", "git", "clone", "--local", "--no-checkout", source, dest); err != nil {
		return nil, fmt.Errorf("clone workspace: %w", err)
	}
	if err := runCommand(dest, "git", "checkout", "--detach", base); err != nil {
		return nil, fmt.Errorf("checkout workspace base commit: %w", err)
	}
	if err := removeGitAlternates(dest); err != nil {
		return nil, err
	}
	if err := removeGitRemotes(dest); err != nil {
		return nil, err
	}
	if err := configureSessionGitIdentity(dest); err != nil {
		return nil, fmt.Errorf("configure cloned workspace git identity: %w", err)
	}

	return &sessionapi.Workspace{
		Mode:       workspaceModeGitClone,
		Source:     source,
		BaseCommit: base,
	}, nil
}

func resolveExtendedWorkspaceArtifact(sessionsRoot string, parent *spec.CompiledEnvironment) (*sessionapi.WorkspaceArtifactBinding, error) {
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("read local sessions: %w", err)
	}

	var best *sessionapi.WorkspaceArtifactBinding
	bestStamp := ""
	currentControllerID := currentLocalControllerSessionID()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsRoot, entry.Name())
		manifest, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
		if err != nil {
			continue
		}
		stamp := manifestCompletionStamp(manifest)
		for _, sessionSpec := range manifest.Specs {
			if sessionSpec.ContentHash == nil || *sessionSpec.ContentHash != parent.ContentHash {
				continue
			}
			if currentControllerID != "" &&
				manifest.SessionID == currentControllerID &&
				manifest.SessionKind == sessionapi.KindController {
				active := activeWorkspacePath(sessionDir)
				ok, err := dirExists(active)
				if err != nil {
					return nil, fmt.Errorf("inspect active workspace %s: %w", active, err)
				}
				if ok {
					return &sessionapi.WorkspaceArtifactBinding{
						SpecPath:      parent.Environment.Path,
						SpecName:      parent.Environment.Name,
						ContentHash:   parent.ContentHash,
						SessionID:     manifest.SessionID,
						WorkspacePath: active,
					}, nil
				}
			}
			if !manifestCompleted(manifest) {
				continue
			}
			if sessionSpec.WorkspacePath == nil || *sessionSpec.WorkspacePath == "" {
				continue
			}
			ok, err := pathExists(*sessionSpec.WorkspacePath)
			if err != nil {
				return nil, fmt.Errorf("inspect workspace artifact %s: %w", *sessionSpec.WorkspacePath, err)
			}
			if !ok {
				continue
			}
			if best != nil && stamp <= bestStamp {
				continue
			}
			bestStamp = stamp
			best = &sessionapi.WorkspaceArtifactBinding{
				SpecPath:      parent.Environment.Path,
				SpecName:      parent.Environment.Name,
				ContentHash:   parent.ContentHash,
				SessionID:     manifest.SessionID,
				WorkspacePath: *sessionSpec.WorkspacePath,
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("extended spec %q (%s) has no completed local workspace artifact; run the parent spec first", parent.Environment.Name, parent.ContentHash)
	}
	return best, nil
}

func currentLocalControllerSessionID() string {
	if strings.TrimSpace(os.Getenv("TELOS_RUNTIME")) != string(sessionapi.RuntimeLocal) {
		return ""
	}
	return strings.TrimSpace(os.Getenv("TELOS_SESSION_ID"))
}

func manifestCompleted(manifest *sessionapi.Manifest) bool {
	if manifest == nil {
		return false
	}
	last := manifest.LastEpoch()
	return last != nil && last.Result != nil && *last.Result == "completed"
}

func manifestCompletionStamp(manifest *sessionapi.Manifest) string {
	if last := manifest.LastEpoch(); last != nil && last.FinishedAt != nil {
		return *last.FinishedAt
	}
	return manifest.CreatedAt
}

func ensureScopeMarker(scope string, sessionsRoot string) error {
	if strings.TrimSpace(os.Getenv("TELOS_SESSION_DIR")) != "" || strings.TrimSpace(scope) == "" {
		return nil
	}

	execRoot := filepath.Dir(sessionsRoot)
	if err := os.MkdirAll(execRoot, 0o755); err != nil {
		return fmt.Errorf("create local workspace root: %w", err)
	}

	marker := filepath.Join(scope, ".telos")
	info, err := os.Lstat(marker)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(marker)
		if err == nil && samePath(resolveLinkTarget(scope, target), execRoot) {
			return nil
		}
		if err := os.Remove(marker); err != nil {
			return fmt.Errorf("replace local session marker: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect local session marker: %w", err)
	}

	if err := os.Symlink(execRoot, marker); err != nil {
		return fmt.Errorf("create local session marker: %w", err)
	}
	return nil
}

func extractWorkspaceArtifact(archivePath string, dest string) error {
	dest, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve extraction target: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, ok := safeArchiveTarget(dest, header.Name)
		if !ok {
			return fmt.Errorf("unsafe archive path: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tarReader); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if !safeArchiveSymlinkTarget(dest, target, header.Linkname) {
				return fmt.Errorf("unsafe archive symlink: %s -> %s", header.Name, header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil && !os.IsExist(err) {
				return err
			}
		}
	}
}

func safeArchiveTarget(dest string, name string) (string, bool) {
	clean := filepath.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.Join(dest, clean), true
}

func safeArchiveSymlinkTarget(dest string, linkPath string, linkname string) bool {
	if strings.TrimSpace(linkname) == "" {
		return false
	}
	target := filepath.Clean(linkname)
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	}
	rel, err := filepath.Rel(dest, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func existingScopeMarker(scope string) (string, bool) {
	marker := filepath.Join(scope, ".telos")
	info, err := os.Stat(marker)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return marker, true
}

func gitDirtyStatus(source string) (string, error) {
	status, err := gitOutput(source, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path := ""
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		if path == ".telos" || strings.HasPrefix(path, ".telos/") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func workspaceUsesLFS(source string) (bool, error) {
	if lfs, err := gitOutput(source, "lfs", "ls-files", "-n"); err == nil && strings.TrimSpace(lfs) != "" {
		return true, nil
	}

	usesLFS := false
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Name() != ".gitattributes" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "filter=lfs") {
			usesLFS = true
			return filepath.SkipAll
		}
		return nil
	})
	return usesLFS, err
}

func removeGitAlternates(workspace string) error {
	alternatesPath := filepath.Join(workspace, ".git", "objects", "info", "alternates")
	if _, err := os.Stat(alternatesPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect git alternates: %w", err)
	}
	if err := runCommand(workspace, "git", "repack", "-ad"); err != nil {
		return fmt.Errorf("materialize git alternates: %w", err)
	}
	if err := os.Remove(alternatesPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove git alternates: %w", err)
	}
	return nil
}

func removeGitRemotes(workspace string) error {
	remotes, err := gitOutput(workspace, "remote")
	if err != nil {
		return fmt.Errorf("list git remotes: %w", err)
	}
	for _, remote := range strings.Fields(remotes) {
		if err := runCommand(workspace, "git", "remote", "remove", remote); err != nil {
			return fmt.Errorf("remove git remote %s: %w", remote, err)
		}
	}
	return nil
}

func initSnapshotRepo(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create session workspace: %w", err)
	}
	if err := runCommand(dir, "git", "init", "-q"); err != nil {
		return "", fmt.Errorf("initialize session workspace git repo: %w", err)
	}
	if err := configureSessionGitIdentity(dir); err != nil {
		return "", fmt.Errorf("configure session workspace git identity: %w", err)
	}
	if err := runCommand(dir, "git", "add", "-A"); err != nil {
		return "", fmt.Errorf("stage session workspace snapshot: %w", err)
	}
	if err := runCommand(dir, "git", "commit", "-q", "--allow-empty", "-m", "Initial workspace snapshot"); err != nil {
		return "", fmt.Errorf("commit session workspace snapshot: %w", err)
	}
	base, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve session workspace base commit: %w", err)
	}
	return strings.TrimSpace(base), nil
}

func configureSessionGitIdentity(workspace string) error {
	if !isGitRepo(workspace) {
		return nil
	}
	if err := runCommand(workspace, "git", "config", "user.name", sessionGitUserName); err != nil {
		return fmt.Errorf("set git user.name: %w", err)
	}
	if err := runCommand(workspace, "git", "config", "user.email", sessionGitUserEmail); err != nil {
		return fmt.Errorf("set git user.email: %w", err)
	}
	return nil
}

func workspaceHeadCommit(dir string) (string, error) {
	if isGitRepo(dir) {
		base, err := gitOutput(dir, "rev-parse", "HEAD")
		if err != nil {
			return "", fmt.Errorf("resolve artifact workspace base commit: %w", err)
		}
		return strings.TrimSpace(base), nil
	}
	return initSnapshotRepo(dir)
}

func copySnapshot(source string, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create session workspace: %w", err)
	}
	if _, err := exec.LookPath("rsync"); err == nil {
		if err := runCommand("", "rsync", "-a", "--delete", "--exclude=.telos", withTrailingSeparator(source), withTrailingSeparator(dest)); err == nil {
			return nil
		}
	}
	return copyDirContents(source, dest)
}

func copyDirContents(source string, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".telos" || strings.HasPrefix(rel, ".telos"+string(os.PathSeparator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source string, dest string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func withTrailingSeparator(path string) string {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}
	return path + string(os.PathSeparator)
}

func isGitRepo(dir string) bool {
	if err := runCommand(dir, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return false
	}
	return true
}

func gitTopLevel(dir string) (string, bool) {
	out, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(out)
	return root, root != ""
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", commandError(cmd, out, err)
	}
	return string(out), nil
}

func runCommand(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return commandError(cmd, out, err)
	}
	return nil
}

func commandError(cmd *exec.Cmd, out []byte, err error) error {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, text)
}

func workspaceID(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:])[:16]
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	evaluated, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return evaluated, nil
}

func resolveLinkTarget(scope string, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(scope, target)
}

func samePath(left string, right string) bool {
	canonicalLeft, err := canonicalPath(left)
	if err == nil {
		left = canonicalLeft
	}
	canonicalRight, err := canonicalPath(right)
	if err == nil {
		right = canonicalRight
	}
	return left == right
}
