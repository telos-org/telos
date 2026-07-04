package executor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (t *nativeTools) readFile(p string, startLine, limitLines int) (toolOutput, error) {
	resolved, err := t.resolvePath(p)
	if err != nil {
		return toolOutput{}, err
	}
	read, err := t.readBoundedTextFile(resolved.full, startLine, limitLines)
	if err != nil {
		return toolOutput{}, err
	}
	if data, err := os.ReadFile(resolved.full); err == nil {
		t.fileTracker.record(resolved.full, data)
	}
	if read.binary {
		return toolOutput{
			fields: toolFields("path", resolved.rel, "size_bytes", read.sizeBytes, "binary", true),
			bodies: []toolBodySection{{Key: "content", Text: "(binary file omitted)"}},
		}, nil
	}
	return toolOutput{
		fields: toolFields(
			"path", resolved.rel,
			"size_bytes", read.sizeBytes,
			"lines_returned", fmt.Sprintf("%d-%d", read.startLine, read.endLine),
			"line_count", read.totalLines,
			"truncated", read.truncated,
		),
		bodies:    []toolBodySection{{Key: "content", Text: read.content}},
		truncated: read.truncated,
	}, nil
}

type boundedReadResult struct {
	sizeBytes     int64
	content       string
	startLine     int
	endLine       int
	totalLines    int
	truncated     bool
	byteTruncated bool
	binary        bool
}

func (t *nativeTools) readBoundedTextFile(full string, startLine, limitLines int) (boundedReadResult, error) {
	info, err := os.Stat(full)
	if err != nil {
		return boundedReadResult{}, err
	}
	if info.IsDir() {
		return boundedReadResult{}, fmt.Errorf("%s is a directory", full)
	}
	if startLine <= 0 {
		startLine = 1
	}
	if limitLines <= 0 {
		limitLines = defaultToolReadLines
	}
	if limitLines > t.limit.MaxLines {
		limitLines = t.limit.MaxLines
	}
	content, totalLines, endLine, truncatedBytes, binary, err := readTextFileRange(full, startLine, limitLines, t.limit.MaxBytes)
	if err != nil {
		return boundedReadResult{}, err
	}
	if binary {
		return boundedReadResult{sizeBytes: info.Size(), binary: true}, nil
	}
	if endLine < startLine {
		endLine = startLine - 1
	}
	return boundedReadResult{
		sizeBytes:     info.Size(),
		content:       content,
		startLine:     startLine,
		endLine:       endLine,
		totalLines:    totalLines,
		truncated:     endLine < totalLines || truncatedBytes,
		byteTruncated: truncatedBytes,
	}, nil
}

func readTextFileRange(full string, startLine, limitLines, maxBytes int) (string, int, int, bool, bool, error) {
	f, err := os.Open(full)
	if err != nil {
		return "", 0, 0, false, false, err
	}
	defer f.Close()

	endLine := startLine + limitLines - 1
	totalLines := 0
	lastReturned := startLine - 1
	truncatedBytes := false
	var out strings.Builder
	reader := bufio.NewReaderSize(f, 64<<10)
	var text utf8TextStream
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if !text.accept(fragment, readErr != bufio.ErrBufferFull) {
				return "", totalLines, lastReturned, false, true, nil
			}
			currentLine := totalLines + 1
			if currentLine >= startLine && currentLine <= endLine {
				lastReturned = currentLine
				if !truncatedBytes {
					remaining := maxBytes - out.Len()
					if remaining <= 0 {
						truncatedBytes = true
					} else if len(fragment) > remaining {
						out.Write(fragment[:validUTF8PrefixLen(fragment, remaining)])
						truncatedBytes = true
					} else {
						out.Write(fragment)
					}
				}
			}
			if readErr != bufio.ErrBufferFull {
				totalLines++
			}
		}
		switch readErr {
		case nil, bufio.ErrBufferFull:
			continue
		case io.EOF:
			content := out.String()
			if !utf8.ValidString(content) {
				content = content[:validUTF8PrefixLen([]byte(content), len(content))]
				truncatedBytes = true
			}
			return content, totalLines, lastReturned, truncatedBytes, false, nil
		default:
			return "", totalLines, lastReturned, truncatedBytes, false, readErr
		}
	}
}

type utf8TextStream struct {
	pending []byte
}

func (s *utf8TextStream) accept(chunk []byte, complete bool) bool {
	if bytes.IndexByte(chunk, 0) >= 0 {
		return false
	}
	if len(s.pending) > 0 {
		combined := make([]byte, 0, len(s.pending)+len(chunk))
		combined = append(combined, s.pending...)
		combined = append(combined, chunk...)
		chunk = combined
		s.pending = nil
	}
	for len(chunk) > 0 {
		r, size := utf8.DecodeRune(chunk)
		if r == utf8.RuneError && size == 1 {
			if !complete && !utf8.FullRune(chunk) {
				s.pending = append(s.pending[:0], chunk...)
				return true
			}
			return false
		}
		chunk = chunk[size:]
	}
	return true
}

func (t *nativeTools) write(p, content string) (toolOutput, error) {
	if p == "" {
		return toolOutput{}, fmt.Errorf("path is required")
	}
	if strings.ContainsRune(content, '\x00') {
		return toolOutput{}, fmt.Errorf("content contains NUL byte")
	}
	resolved, err := t.resolveWritePath(p)
	if err != nil {
		return toolOutput{}, err
	}
	created := false
	if _, statErr := os.Stat(resolved.full); os.IsNotExist(statErr) {
		created = true
	}
	if !created {
		if err := ensureNoStaleWrite(t.fileTracker, resolved.full, resolved.rel); err != nil {
			return toolOutput{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(resolved.full), 0o755); err != nil {
		return toolOutput{}, err
	}
	if err := t.scope.recheckWriteTarget(resolved.full, p); err != nil {
		return toolOutput{}, err
	}
	if err := os.WriteFile(resolved.full, []byte(content), 0o644); err != nil {
		return toolOutput{}, err
	}
	t.fileTracker.record(resolved.full, []byte(content))
	fields := toolFields("path", resolved.rel, "created", created, "bytes_written", len(content))
	if info, err := os.Stat(resolved.full); err == nil {
		fields = append(fields, toolField{Key: "mode", Value: info.Mode().String()})
	}
	return toolOutput{fields: fields, changedFiles: []string{resolved.rel}, preview: changedFilePreview(content)}, nil
}

func (t *nativeTools) edit(p, oldString, newString string, replaceAll bool, expectedCount int) (toolOutput, error) {
	if oldString == "" {
		return toolOutput{}, fmt.Errorf("old_string is required")
	}
	if strings.ContainsRune(oldString, '\x00') || strings.ContainsRune(newString, '\x00') {
		return toolOutput{}, fmt.Errorf("old_string and new_string must not contain NUL bytes")
	}
	resolved, err := t.resolveWritePath(p)
	if err != nil {
		return toolOutput{}, err
	}
	if err := ensureNoStaleWrite(t.fileTracker, resolved.full, resolved.rel); err != nil {
		return toolOutput{}, err
	}
	data, err := os.ReadFile(resolved.full)
	if err != nil {
		return toolOutput{}, err
	}
	if !isUTF8TextBytes(data) {
		return toolOutput{}, fmt.Errorf("%s is not a UTF-8 text file", p)
	}
	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return toolOutput{}, fmt.Errorf("old_string not found in %s", p)
	}
	if expectedCount > 0 && count != expectedCount {
		return toolOutput{}, fmt.Errorf("replacement count mismatch in %s: found %d, expected %d", p, count, expectedCount)
	}
	n := 1
	if replaceAll {
		n = -1
	} else if expectedCount > 1 {
		return toolOutput{}, fmt.Errorf("expected_count=%d requires replace_all=true", expectedCount)
	}
	updated := strings.Replace(text, oldString, newString, n)
	if err := t.scope.recheckWriteTarget(resolved.full, p); err != nil {
		return toolOutput{}, err
	}
	if err := os.WriteFile(resolved.full, []byte(updated), 0o644); err != nil {
		return toolOutput{}, err
	}
	t.fileTracker.record(resolved.full, []byte(updated))
	if !replaceAll {
		count = 1
	}
	fields := toolFields("path", resolved.rel, "replacement_count", count, "bytes_written", len(updated), "created", false)
	if info, err := os.Stat(resolved.full); err == nil {
		fields = append(fields, toolField{Key: "mode", Value: info.Mode().String()})
	}
	return toolOutput{fields: fields, changedFiles: []string{resolved.rel}, preview: compactDiffPreview(text, updated)}, nil
}

func (t *nativeTools) fileInfo(p string) (toolOutput, error) {
	resolved, err := t.resolvePath(p)
	if err != nil {
		return toolOutput{}, err
	}
	info, err := os.Stat(resolved.full)
	if err != nil {
		return toolOutput{}, err
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	fields := toolFields("path", resolved.rel, "type", kind, "size_bytes", info.Size(), "mode", info.Mode().String())
	if info.Mode().IsRegular() && info.Size() <= 8<<20 {
		if data, err := os.ReadFile(resolved.full); err == nil && isUTF8TextBytes(data) {
			fields = append(fields, toolField{Key: "line_count", Value: len(strings.Split(string(data), "\n"))})
		}
	}
	return toolOutput{fields: fields}, nil
}

func (t *nativeTools) resolvePath(p string) (scopedPath, error) {
	if t.scope == nil {
		return scopedPath{}, fmt.Errorf("workspace scope unavailable")
	}
	return t.scope.resolveExisting(p)
}

func (t *nativeTools) resolveWritePath(p string) (scopedPath, error) {
	if t.scope == nil {
		return scopedPath{}, fmt.Errorf("workspace scope unavailable")
	}
	return t.scope.resolveWriteTarget(p)
}

func (t *nativeTools) displayPath(full string) string {
	if t.scope != nil {
		if rel, err := t.scope.relative(full, full); err == nil {
			return rel
		}
	}
	return filepath.ToSlash(full)
}

func (t *nativeTools) logOutsideWorkspaceAttempt(action, requested string, write bool) {
	if t == nil {
		return
	}
	_ = t.logger.outsideWorkspaceAccess(action, filepath.ToSlash(requested), write)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".telos", "__pycache__", "node_modules", ".venv", "venv":
		return true
	default:
		return false
	}
}
