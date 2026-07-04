package executor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func (t *nativeTools) skill(action, name, refPath string, startLine, limitLines int) (toolOutput, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		if len(t.skills) == 0 {
			return toolOutput{bodies: []toolBodySection{{Text: "skills: none"}}}, nil
		}
		names := make([]string, 0, len(t.skills))
		for name := range t.skills {
			names = append(names, name)
		}
		sort.Strings(names)
		var lines []string
		for _, skillName := range names {
			ref := t.skills[skillName]
			required := ""
			if ref.Required {
				required = "\nrequired: true"
			}
			lines = append(lines, fmt.Sprintf("name: %s%s\ndescription: %s\npath: %s", ref.Name, required, ref.Description, ref.Path))
		}
		return toolOutput{bodies: []toolBodySection{{Text: strings.Join(lines, "\n---\n")}}}, nil
	case "read", "read_ref":
		ref, ok := t.skills[strings.TrimSpace(name)]
		if !ok {
			return toolOutput{}, fmt.Errorf("unknown skill %q; use action=list to inspect available skills", name)
		}
		readPath := ref.Path
		if action == "read_ref" {
			var err error
			readPath, err = resolveSkillReferencePath(ref.Path, refPath)
			if err != nil {
				return toolOutput{}, err
			}
		}
		read, err := t.readSkillFile(readPath, startLine, limitLines)
		if err != nil {
			return toolOutput{}, err
		}
		if action == "read" && !read.binary && (!read.byteTruncated || read.totalLines <= 1) {
			covered := t.skillCoverage[ref.Name]
			if read.startLine <= covered+1 && read.endLine > covered {
				covered = read.endLine
				t.skillCoverage[ref.Name] = covered
			}
			fullyRead := read.totalLines > 0 && covered >= read.totalLines
			if fullyRead && !t.openedSkills[ref.Name] {
				t.openedSkills[ref.Name] = true
				_ = t.logger.skillApplied(ref.Name, readPath)
			}
		}
		_ = t.logger.skillOpened(ref.Name, readPath, read.truncated)
		return toolOutput{
			fields: toolFields("name", ref.Name, "path", readPath, "truncated", read.truncated),
			bodies: []toolBodySection{{Text: read.body}},
		}, nil
	default:
		return toolOutput{}, fmt.Errorf("unknown skill action %q; use 'list', 'read', or 'read_ref'", action)
	}
}

// skillReadResult carries the rendered skill body plus the line range read, so
// callers can track how much of a required rubric has been covered.
type skillReadResult struct {
	body       string
	startLine  int
	endLine    int
	totalLines int
	truncated  bool
	// byteTruncated is true when the returned content was cut mid-window by the
	// byte cap, so lines [startLine, endLine] were not all actually delivered.
	byteTruncated bool
	binary        bool
}

func (t *nativeTools) readSkillFile(full string, startLine, limitLines int) (skillReadResult, error) {
	read, err := t.readBoundedTextFile(full, startLine, limitLines)
	if err != nil {
		return skillReadResult{}, err
	}
	if read.binary {
		return skillReadResult{
			body:   fmt.Sprintf("size_bytes: %d\nbinary: true\ncontent:\n(binary file omitted)", read.sizeBytes),
			binary: true,
		}, nil
	}
	body := fmt.Sprintf("size_bytes: %d\nlines_returned: %d-%d\nline_count: %d\ncontent:\n%s", read.sizeBytes, read.startLine, read.endLine, read.totalLines, read.content)
	return skillReadResult{
		body:          body,
		startLine:     read.startLine,
		endLine:       read.endLine,
		totalLines:    read.totalLines,
		truncated:     read.truncated,
		byteTruncated: read.byteTruncated,
	}, nil
}

func resolveSkillReferencePath(skillPath, refPath string) (string, error) {
	refPath = strings.TrimSpace(refPath)
	if refPath == "" {
		return "", fmt.Errorf("path is required for skill action='read_ref'")
	}
	refPath = filepath.FromSlash(refPath)
	if filepath.IsAbs(refPath) {
		return "", fmt.Errorf("skill reference path %q must be relative", refPath)
	}
	base, err := filepath.Abs(filepath.Dir(skillPath))
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, filepath.Clean(refPath)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill reference path %q is outside skill directory", refPath)
	}
	return full, nil
}

func (t *nativeTools) missingRequiredSkills() []string {
	if t == nil {
		return nil
	}
	var missing []string
	for name, ref := range t.skills {
		if ref.Required && !t.openedSkills[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
