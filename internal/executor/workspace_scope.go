package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspaceScope struct {
	root string
}

type scopedPath struct {
	full string
	rel  string
}

func newWorkspaceScope(root string) (*workspaceScope, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &workspaceScope{root: resolved}, nil
}

func (s *workspaceScope) resolveExisting(requested string) (scopedPath, error) {
	if strings.TrimSpace(requested) == "" {
		requested = "."
	}
	target, err := s.absoluteRequestedPath(requested)
	if err != nil {
		return scopedPath{}, err
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return scopedPath{}, err
	}
	rel, err := s.relative(resolved, requested)
	if err != nil {
		return scopedPath{}, err
	}
	return scopedPath{full: resolved, rel: rel}, nil
}

func (s *workspaceScope) resolveWriteTarget(requested string) (scopedPath, error) {
	if strings.TrimSpace(requested) == "" {
		return scopedPath{}, fmt.Errorf("path is required")
	}
	target, err := s.absoluteRequestedPath(requested)
	if err != nil {
		return scopedPath{}, err
	}
	if err := s.recheckWriteTarget(target, requested); err != nil {
		return scopedPath{}, err
	}

	existing := target
	var missing []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if os.IsNotExist(err) {
			parent := filepath.Dir(existing)
			if parent == existing {
				return scopedPath{}, err
			}
			missing = append([]string{filepath.Base(existing)}, missing...)
			existing = parent
		} else {
			return scopedPath{}, err
		}
	}

	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return scopedPath{}, err
	}
	for _, segment := range missing {
		resolved = filepath.Join(resolved, segment)
	}
	rel, err := s.relative(resolved, requested)
	if err != nil {
		return scopedPath{}, err
	}
	return scopedPath{full: resolved, rel: rel}, nil
}

func (s *workspaceScope) absoluteRequestedPath(requested string) (string, error) {
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(s.root, target)
	}
	return filepath.Abs(filepath.Clean(target))
}

func (s *workspaceScope) relative(full string, requested string) (string, error) {
	rel, err := filepath.Rel(s.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", outsideWorkspaceError(requested)
	}
	if rel == "." {
		return ".", nil
	}
	return filepath.ToSlash(rel), nil
}

func (s *workspaceScope) recheckWriteTarget(target string, requested string) error {
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return outsideWorkspaceError(requested)
	}
	if rel == "." {
		return nil
	}
	current := s.root
	for _, segment := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			symlinkRel, relErr := filepath.Rel(s.root, current)
			if relErr != nil {
				symlinkRel = current
			}
			return fmt.Errorf("%s must not traverse symlink %s", requested, filepath.ToSlash(symlinkRel))
		}
	}
	return nil
}

func (s *workspaceScope) contains(full string) bool {
	rel, err := filepath.Rel(s.root, full)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func outsideWorkspaceError(requestedPath string) error {
	return fmt.Errorf("%s must stay inside the workspace", requestedPath)
}
