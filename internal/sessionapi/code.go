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

// ErrNoCode is returned when a session's workspace has no files to download.
var ErrNoCode = errors.New("no code in the workspace yet")

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

// Code is a snapshot of the files in a session's workspace.
type Code struct {
	workspace string
	tree      string
}

// Code snapshots the files in the session's workspace as they are now,
// committed or not, minus codeExcludes and anything .gitignore ignores. It
// returns ErrNoCode when no files are left.
func (fs *FileStore) Code(id string) (*Code, error) {
	if !fileExists(fs.manifestPath(id)) {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	workspace := workspaceDir(fs.sessionDir(id))
	// Without its own repository, git would snapshot an enclosing one.
	if !fileExists(filepath.Join(workspace, ".git")) {
		return nil, ErrNoCode
	}
	// Stage into a throwaway index so the agent's own index stays untouched.
	tmp, err := os.MkdirTemp("", "telos-code-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	index := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := gitOutput(workspace, index, append([]string{"add", "-A", "--"}, codeExcludes...)...); err != nil {
		return nil, err
	}
	tree, err := gitOutput(workspace, index, "write-tree")
	if err != nil {
		return nil, err
	}
	emptyTree, err := gitOutput(workspace, nil, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		return nil, err
	}
	if tree == emptyTree {
		return nil, ErrNoCode
	}
	return &Code{workspace: workspace, tree: tree}, nil
}

// workspaceDir is where a session's agent works: a git repository.
func workspaceDir(sessionDir string) string {
	return filepath.Join(sessionDir, "workspace")
}

// WriteZip streams the snapshot as a zip.
func (c *Code) WriteZip(ctx context.Context, w io.Writer) error {
	return git(ctx, c.workspace, nil, w, "archive", "--format=zip", c.tree)
}

func gitOutput(dir string, env []string, args ...string) (string, error) {
	var out bytes.Buffer
	err := git(context.Background(), dir, env, &out, args...)
	return strings.TrimSpace(out.String()), err
}

func git(ctx context.Context, dir string, env []string, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
