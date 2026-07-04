package executor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

func (t *nativeTools) applyPatch(ctx context.Context, patchText string) (toolOutput, error) {
	if strings.TrimSpace(patchText) == "" {
		return toolOutput{}, fmt.Errorf("patch is required")
	}
	tmp, err := os.CreateTemp("", "telos-patch-*.diff")
	if err != nil {
		return toolOutput{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(patchText); err != nil {
		tmp.Close()
		return toolOutput{}, err
	}
	if err := tmp.Close(); err != nil {
		return toolOutput{}, err
	}
	metadata := parsePatchMetadata(patchText)
	if err := validatePatchPaths(metadata.declared); err != nil {
		return toolOutput{}, err
	}
	cmd := "git apply --whitespace=nowarn --recount " + shellQuote(tmp.Name())
	bashOut, err := t.bash(ctx, cmd, "", nil, defaultToolTimeoutSec)
	if err != nil {
		return bashOut, err
	}
	return toolOutput{
		fields: toolFields(
			"patch_bytes", len(patchText),
			"changed_path_count", len(metadata.changed),
			"changed_paths", strings.Join(metadata.changed, ", "),
			"created_paths", strings.Join(metadata.created, ", "),
			"deleted_paths", strings.Join(metadata.deleted, ", "),
			"hunk_count", metadata.hunks,
		),
		bodies: []toolBodySection{
			{Key: "files", Text: t.patchFileMetadata(metadata.changed, metadata.created, metadata.deleted)},
			{Text: bashOut.innerText()},
		},
	}, nil
}

func (t *nativeTools) patchFileMetadata(changed, created, deleted []string) string {
	createdSet := stringSet(created)
	deletedSet := stringSet(deleted)
	var lines []string
	for _, p := range changed {
		full, err := t.resolvePath(p)
		if err != nil {
			lines = append(lines, fmt.Sprintf("- path: %s\n  error: %s", p, err))
			continue
		}
		if deletedSet[p] {
			lines = append(lines, fmt.Sprintf("- path: %s\n  created: false\n  deleted: true\n  bytes_written: 0", t.displayPath(full)))
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			lines = append(lines, fmt.Sprintf("- path: %s\n  created: %t\n  deleted: false\n  error: %s", t.displayPath(full), createdSet[p], err))
			continue
		}
		lines = append(lines, fmt.Sprintf("- path: %s\n  created: %t\n  deleted: false\n  bytes_written: %d\n  mode: %s", t.displayPath(full), createdSet[p], info.Size(), info.Mode().String()))
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type patchMetadata struct {
	changed  []string
	declared []string
	created  []string
	deleted  []string
	hunks    int
}

func parsePatchMetadata(patchText string) patchMetadata {
	var metadata patchMetadata
	changedSeen := map[string]bool{}
	declaredSeen := map[string]bool{}
	createdSeen := map[string]bool{}
	deletedSeen := map[string]bool{}
	var previous string

	addChanged := func(p string) {
		if p == "" || p == "/dev/null" || changedSeen[p] {
			return
		}
		changedSeen[p] = true
		metadata.changed = append(metadata.changed, p)
	}
	addDeclared := func(p string) {
		p = normalizePatchPath(p)
		if p == "" || p == "/dev/null" || declaredSeen[p] {
			return
		}
		declaredSeen[p] = true
		metadata.declared = append(metadata.declared, p)
	}
	addCreated := func(p string) {
		if p == "" || createdSeen[p] {
			return
		}
		createdSeen[p] = true
		metadata.created = append(metadata.created, p)
	}
	addDeleted := func(p string) {
		if p == "" || deletedSeen[p] {
			return
		}
		deletedSeen[p] = true
		metadata.deleted = append(metadata.deleted, p)
	}

	scanner := bufio.NewScanner(strings.NewReader(patchText))
	scanner.Buffer(make([]byte, 0, 64*1024), len(patchText)+1)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			p := strings.TrimPrefix(line, "+++ b/")
			addChanged(p)
			addDeclared(p)
			if strings.TrimSpace(previous) == "--- /dev/null" {
				addCreated(p)
			}
		case strings.HasPrefix(line, "--- a/"):
			p := strings.TrimPrefix(line, "--- a/")
			addChanged(p)
			addDeclared(p)
		case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- "):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				addDeclared(fields[1])
			}
		case strings.HasPrefix(line, "diff --git "):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				addDeclared(fields[2])
				addDeclared(fields[3])
			}
		case strings.HasPrefix(line, "rename from "):
			addDeclared(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			addDeclared(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "copy from "):
			addDeclared(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			addDeclared(strings.TrimPrefix(line, "copy to "))
		}
		if strings.TrimSpace(line) == "+++ /dev/null" && strings.HasPrefix(previous, "--- a/") {
			addDeleted(strings.TrimPrefix(previous, "--- a/"))
		}
		if strings.HasPrefix(line, "@@ ") {
			metadata.hunks++
		}
		previous = line
	}

	sort.Strings(metadata.changed)
	sort.Strings(metadata.declared)
	sort.Strings(metadata.created)
	sort.Strings(metadata.deleted)
	return metadata
}

func normalizePatchPath(p string) string {
	p = strings.TrimSpace(p)
	if unquoted, err := strconv.Unquote(p); err == nil {
		p = unquoted
	}
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}

func validatePatchPaths(paths []string) error {
	for _, p := range paths {
		clean := path.Clean(strings.TrimSpace(p))
		if clean == "." || clean == "" {
			return fmt.Errorf("patch contains empty path")
		}
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("patch path %q is outside workspace", p)
		}
	}
	return nil
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}
