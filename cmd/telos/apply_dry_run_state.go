package main

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/spec"
)

type previewSpecState struct {
	Name            string             `json:"name,omitempty"`
	Version         string             `json:"version,omitempty"`
	IntervalSeconds *int               `json:"interval_seconds,omitempty"`
	Skills          []previewSkillLock `json:"skills,omitempty"`
}

type previewSkillLock struct {
	Name    string `json:"name"`
	Ref     string `json:"ref,omitempty"`
	Digest  string `json:"digest"`
	Starred bool   `json:"required_rubric,omitempty"`
}

func previewSpecStateFromMarkdown(
	markdown []byte,
	manifest *spec.ApplyPackageManifest,
) (previewSpecState, error) {
	raw, _, ok := spec.ParseFrontmatter(string(markdown))
	if !ok {
		return previewSpecState{}, fmt.Errorf("spec has no valid YAML frontmatter")
	}
	state := previewSpecState{Skills: []previewSkillLock{}}
	if name, ok := raw["name"].(string); ok {
		state.Name = strings.TrimSpace(name)
	}
	if version, ok := raw["version"].(string); ok {
		state.Version = strings.TrimSpace(version)
	}
	if interval, ok := raw["interval"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(fmt.Sprint(interval)))
		if err != nil || duration < 0 || duration%time.Second != 0 {
			return previewSpecState{}, fmt.Errorf("invalid interval %q", interval)
		}
		seconds := int(duration / time.Second)
		state.IntervalSeconds = &seconds
	}
	if manifest == nil {
		return state, nil
	}
	names := make([]string, 0, len(manifest.Skills))
	for name := range manifest.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lock := manifest.Skills[name]
		ref := strings.TrimSpace(lock.Ref)
		if ref == "" {
			ref = strings.TrimSpace(manifest.SkillProvenance[name].Ref)
		}
		state.Skills = append(state.Skills, previewSkillLock{
			Name:    name,
			Ref:     ref,
			Digest:  strings.TrimSpace(lock.Digest),
			Starred: lock.Starred,
		})
	}
	return state, nil
}

func printPreviewStateDelta(out io.Writer, current, proposed previewSpecState) {
	currentInterval := formatPreviewInterval(current.IntervalSeconds)
	proposedInterval := formatPreviewInterval(proposed.IntervalSeconds)
	versionChanged := current.Version != proposed.Version
	intervalChanged := currentInterval != proposedInterval
	skillsChanged := !slices.Equal(current.Skills, proposed.Skills)
	if !versionChanged && !intervalChanged && !skillsChanged {
		return
	}
	if versionChanged {
		printSummaryField(out, "Version", previewDeltaValue(current.Version, proposed.Version))
	}
	if intervalChanged {
		printSummaryField(out, "Interval", previewDeltaValue(currentInterval, proposedInterval))
	}
	if skillsChanged {
		if versionChanged || intervalChanged {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "Skill locks")
		printDetailField(out, "current", formatPreviewSkillLocks(current.Skills))
		printDetailField(out, "proposed", formatPreviewSkillLocks(proposed.Skills))
	}
}

func previewDeltaValue(current, proposed string) string {
	return firstNonEmpty(current, "-") + " -> " + firstNonEmpty(proposed, "-")
}

// formatPreviewInterval writes an interval the way specs do, such as 6h or 1h30m.
func formatPreviewInterval(seconds *int) string {
	if seconds == nil {
		return "-"
	}
	value := (time.Duration(*seconds) * time.Second).String()
	if strings.HasSuffix(value, "m0s") {
		value = strings.TrimSuffix(value, "0s")
	}
	if strings.HasSuffix(value, "h0m") {
		value = strings.TrimSuffix(value, "0m")
	}
	return value
}

func formatPreviewSkillLocks(skills []previewSkillLock) string {
	if len(skills) == 0 {
		return "-"
	}
	values := make([]string, 0, len(skills))
	for _, skill := range skills {
		value := skill.Name
		if skill.Ref != "" {
			value += " " + skill.Ref
		}
		value += " " + skill.Digest
		if skill.Starred {
			value += " *"
		}
		values = append(values, value)
	}
	return strings.Join(values, ", ")
}
