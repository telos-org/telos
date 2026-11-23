package sessionapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoCode is returned when a session's workspace has no committed files to
// download.
var ErrNoCode = errors.New("no code committed yet")

// codeExcludes keeps Telos's own files, secrets, and reinstallable
// dependencies out of a code download. Every .env file goes, templates
// included: an agent may have written real values into any of them.
var codeExcludes = []string{
	":(exclude)" + dashboardDocFilename,
	":(exclude,glob)**/.telos/**",
	":(exclude,glob)**/.env",
	":(exclude,glob)**/.env.*",
	":(exclude,glob)**/.env/**",
	":(exclude,glob)**/.envrc",
	":(exclude,glob)**/node_modules/**",
	":(exclude,glob)**/.venv/**",
	":(exclude,glob)**/venv/**",
	":(exclude,glob)**/__pycache__/**",
	":(exclude,glob)**/.cache/**",
}

// Code is the last commit in a session's workspace.
type Code struct {
	workspace string
	// Commit is the commit's full ID.
	Commit string
}

// Code returns the last commit in the session's workspace. It returns
// ErrNoCode when that commit has no files to download.
func (fs *FileStore) Code(id string) (*Code, error) {
	if !fileExists(fs.manifestPath(id)) {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	workspace := workspaceDir(fs.sessionDir(id))
	if !fileExists(filepath.Join(workspace, ".git")) {
		return nil, ErrNoCode
	}
	commit, err := gitOutput(workspace, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if exitCode(err) == 1 {
		return nil, ErrNoCode // HEAD names no commit yet
	}
	if err != nil {
		return nil, err
	}
	// Exit status 1 means the commit holds files the zip would keep. ls-tree
	// rejects :(exclude) pathspecs, so compare against the empty tree instead.
	emptyTree, err := gitOutput(workspace, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		return nil, err
	}
	_, err = gitOutput(workspace, append([]string{"diff-tree", "-r", "--quiet", emptyTree, commit, "--"}, codeExcludes...)...)
	switch exitCode(err) {
	case 1:
		return &Code{workspace: workspace, Commit: commit}, nil
	case 0:
		return nil, ErrNoCode
	default:
		return nil, err
	}
}

// workspaceDir is where a session's agent works: a git repository.
func workspaceDir(sessionDir string) string {
	return filepath.Join(sessionDir, "workspace")
}

// WriteZip streams the commit's files, minus codeExcludes, as a zip.
func (c *Code) WriteZip(ctx context.Context, w io.Writer) error {
	return git(ctx, c.workspace, w, append([]string{"archive", "--format=zip", c.Commit, "--"}, codeExcludes...)...)
}

func gitOutput(dir string, args ...string) (string, error) {
	var out bytes.Buffer
	err := git(context.Background(), dir, &out, args...)
	return strings.TrimSpace(out.String()), err
}

func git(ctx context.Context, dir string, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// exitCode is git's exit status: 0 on success, -1 when git didn't run.
func exitCode(err error) int {
	var exit *exec.ExitError
	if err == nil {
		return 0
	}
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
