package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func (t *nativeTools) ls(p string) (toolOutput, error) {
	full, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return toolOutput{}, err
	}
	t.logOutsideWorkspaceAccess("list_dir", full, false)
	dir, err := os.Open(full)
	if err != nil {
		return toolOutput{}, err
	}
	defer dir.Close()
	var lines []string
	entryCount := 0
	for {
		entries, err := dir.ReadDir(256)
		if len(entries) > 0 {
			for _, entry := range entries {
				entryCount++
				if len(lines) >= t.limit.MaxLines {
					continue
				}
				name := entry.Name()
				if entry.IsDir() {
					name += "/"
				}
				lines = append(lines, name)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return toolOutput{}, err
		}
	}
	sort.Strings(lines)
	entriesText, truncatedBytes := truncateText(strings.Join(lines, "\n"), t.limit.MaxBytes)
	truncated := entryCount > len(lines) || truncatedBytes
	return toolOutput{
		fields: toolFields(
			"path", t.displayPath(full),
			"entry_count", entryCount,
			"entries_returned", len(lines),
			"truncated", truncated,
		),
		bodies:    []toolBodySection{{Key: "entries", Text: entriesText}},
		truncated: truncated,
	}, nil
}

func (t *nativeTools) grep(pattern, p string, maxMatches int) (toolOutput, error) {
	if pattern == "" {
		return toolOutput{}, fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 500 {
		maxMatches = 100
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return toolOutput{}, err
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return toolOutput{}, err
	}
	t.logOutsideWorkspaceAccess("search_text", root, false)
	var matches []string
	visit := func(file string) {
		if len(matches) >= maxMatches {
			return
		}
		info, err := os.Stat(file)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			return
		}
		data, err := os.ReadFile(file)
		if err != nil || !isUTF8TextBytes(data) {
			return
		}
		rel := t.displayPath(file)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				line, _ = truncateText(line, defaultToolSearchLineBytes)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
				if len(matches) >= maxMatches {
					break
				}
			}
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return toolOutput{}, err
	}
	if !info.IsDir() {
		visit(root)
	} else {
		_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if len(matches) >= maxMatches {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if shouldSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			visit(file)
			return nil
		})
	}
	if len(matches) == 0 {
		return toolOutput{bodies: []toolBodySection{{Text: "no matches"}}}, nil
	}
	out := strings.Join(matches, "\n")
	out, truncatedBytes := truncateText(out, t.limit.MaxBytes)
	truncated := len(matches) >= maxMatches || truncatedBytes
	return toolOutput{
		fields:    toolFields("match_count", len(matches), "truncated", truncated),
		bodies:    []toolBodySection{{Key: "matches", Text: out}},
		truncated: truncated,
	}, nil
}

func (t *nativeTools) find(pattern, p string, maxMatches int) (toolOutput, error) {
	if pattern == "" {
		return toolOutput{}, fmt.Errorf("pattern is required")
	}
	if err := doublestar.ValidatePattern(pattern); !err {
		return toolOutput{}, fmt.Errorf("invalid glob pattern %q", pattern)
	}
	if maxMatches <= 0 || maxMatches > 1000 {
		maxMatches = 200
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return toolOutput{}, err
	}
	t.logOutsideWorkspaceAccess("find_files", root, false)
	var matches []string
	_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(matches) >= maxMatches {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := t.displayPath(file)
		base := path.Base(rel)
		if ok, _ := doublestar.Match(pattern, rel); ok {
			matches = append(matches, rel)
		} else if ok, _ := doublestar.Match(pattern, base); ok {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) == 0 {
		return toolOutput{bodies: []toolBodySection{{Text: "no matches"}}}, nil
	}
	pathsText, truncatedBytes := truncateText(strings.Join(matches, "\n"), t.limit.MaxBytes)
	truncated := len(matches) >= maxMatches || truncatedBytes
	return toolOutput{
		fields:    toolFields("match_count", len(matches), "truncated", truncated),
		bodies:    []toolBodySection{{Key: "paths", Text: pathsText}},
		truncated: truncated,
	}, nil
}
