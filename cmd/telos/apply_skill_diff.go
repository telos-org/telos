package main

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"unicode/utf8"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type previewSkillFileDiff struct {
	Skill          string `json:"skill"`
	Path           string `json:"path"`
	Change         string `json:"change"`
	OldMode        string `json:"old_mode,omitempty"`
	NewMode        string `json:"new_mode,omitempty"`
	Diff           string `json:"diff,omitempty"`
	ContentOmitted string `json:"content_omitted,omitempty"`
}

func previewSkillChanges(control *cloud.Client, preview *applyPreview, includeDiff bool) {
	current := map[string]string{}
	var currentData []byte
	if preview.Current != nil {
		currentData = preview.Current.data
		for _, skill := range preview.Current.Skills {
			current[skill.Name] = skill.Digest
		}
	}
	changed := map[string]bool{}
	for _, skill := range preview.Proposed.Skills {
		if current[skill.Name] != skill.Digest {
			changed[skill.Name] = true
		}
		delete(current, skill.Name)
	}
	for name := range current {
		changed[name] = true
	}
	if len(changed) == 0 {
		return
	}
	if !includeDiff {
		preview.Notes = append(preview.Notes, "Use --diff to include skill file changes.")
		return
	}
	before, err := previewSkillFiles(control, currentData, changed)
	if err != nil {
		preview.Notes = append(preview.Notes, fmt.Sprintf("Deployed skill file diffs are unavailable: %v", err))
		return
	}
	after, err := previewSkillFiles(control, preview.Proposed.data, changed)
	if err != nil {
		preview.Notes = append(preview.Notes, fmt.Sprintf("Proposed skill file diffs are unavailable: %v", err))
		return
	}
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		preview.SkillDiffs = append(preview.SkillDiffs, diffSkillFiles(name, before[name], after[name])...)
	}
}

func previewSkillFiles(control *cloud.Client, data []byte, names map[string]bool) (map[string]map[string]cloud.SkillFile, error) {
	result := map[string]map[string]cloud.SkillFile{}
	if len(data) == 0 {
		return result, nil
	}
	hydrated, manifest, err := spec.HydrateApplyPackage(data, registrySkillFetcher(control))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "telos-skill-diff-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := spec.ExtractApplyPackage(hydrated, dir); err != nil {
		return nil, err
	}
	for name := range names {
		if _, exists := manifest.Skills[name]; !exists {
			continue
		}
		files, err := readSkillPublishFiles(filepath.Join(dir, "skills", name))
		if err != nil {
			return nil, err
		}
		result[name] = files
	}
	return result, nil
}

func diffSkillFiles(name string, before, after map[string]cloud.SkillFile) []previewSkillFileDiff {
	paths := map[string]bool{}
	for path := range before {
		paths[path] = true
	}
	for path := range after {
		paths[path] = true
	}
	var changes []previewSkillFileDiff
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		old, hadOld := before[path]
		proposed, hasNew := after[path]
		if hadOld && hasNew && old.Mode == proposed.Mode && bytes.Equal(old.Data, proposed.Data) {
			continue
		}
		change := previewSkillFileDiff{Skill: name, Path: path, Change: "modified", OldMode: old.Mode, NewMode: proposed.Mode}
		if !hadOld {
			change.Change = "added"
		} else if !hasNew {
			change.Change = "removed"
		}
		switch {
		case !utf8.Valid(old.Data) || !utf8.Valid(proposed.Data) || bytes.IndexByte(old.Data, 0) >= 0 || bytes.IndexByte(proposed.Data, 0) >= 0:
			change.ContentOmitted = "binary content"
		case len(old.Data)+len(proposed.Data) > 256*1024:
			change.ContentOmitted = "content exceeds 256 KiB"
		default:
			oldPath := "deployed/skills/" + name + "/" + path
			newPath := "proposed/skills/" + name + "/" + path
			if !hadOld {
				oldPath = "/dev/null"
			}
			if !hasNew {
				newPath = "/dev/null"
			}
			change.Diff = string(internaldiff.Diff(oldPath, old.Data, newPath, proposed.Data))
		}
		changes = append(changes, change)
	}
	return changes
}
