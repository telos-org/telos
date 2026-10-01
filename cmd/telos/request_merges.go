package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

type requestConflictChoice struct{ Path, Choice string }
type requestConflictChoices []requestConflictChoice

func (c *requestConflictChoices) String() string { return "" }
func (c *requestConflictChoices) Set(value string) error {
	index := strings.LastIndex(value, "=")
	name, choice, ok := "", "", index > 0
	if ok {
		name, choice = value[:index], value[index+1:]
	}
	if !ok || !validMergePath(name) || (choice != "current" && choice != "proposed" && choice != "local") {
		return fmt.Errorf("use --resolve PATH=current|proposed|local for a reported conflict")
	}
	for _, previous := range *c {
		if previous.Path == name {
			return fmt.Errorf("duplicate resolution for %s", name)
		}
	}
	*c = append(*c, requestConflictChoice{name, choice})
	return nil
}

type requestMergeError struct {
	code, message, workspace string
	merge                    *cloud.RequestMerge
	paths                    map[string]string
}

func (e *requestMergeError) Error() string { return e.message }
func reportRequestPlanError(err error, jsonOut bool) {
	if !jsonOut {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return
	}
	code := "request_failed"
	var apiError *cloud.APIError
	if errors.As(err, &apiError) && apiError.Code != "" {
		code = apiError.Code
	}
	receipt := map[string]any{}
	var mergeError *requestMergeError
	if errors.As(err, &mergeError) {
		code = mergeError.code
		if mergeError.workspace != "" {
			receipt["workspace"] = mergeError.workspace
		}
		if mergeError.merge != nil {
			receipt["merge"] = mergeError.merge
			receipt["local_paths"] = mergeError.paths
		}
	}
	receipt["error"] = map[string]string{"code": code, "message": err.Error()}
	printJSON(receipt)
}

func requestMergeConflictError(workspace *requestMergeWorkspace, unresolved []cloud.MergeConflict) error {
	lines := []string{"resolve conflicts, then rerun the same telos plan command:"}
	for _, conflict := range unresolved {
		local := workspace.localPath(conflict.Path)
		if conflict.Kind == "content" {
			lines = append(lines, "  "+local+" (edit the conflict markers)")
		} else {
			options := "current|proposed|local"
			if conflict.Kind == "binary" || conflict.Kind == "metadata" {
				options = "current|proposed"
			}
			lines = append(lines, fmt.Sprintf("  %s (%s): --resolve '%s=%s'", local, conflict.Kind, conflict.Path, options))
		}
	}
	lines = append(lines, "The request has not been updated. --resolve selects the current deployment, your uploaded proposal, or an explicitly edited local file/deletion. Binary conflicts require current or proposed.")
	paths := map[string]string{}
	for _, file := range workspace.Merge.Files {
		paths[file.Path] = workspace.localPath(file.Path)
	}
	return &requestMergeError{code: "merge_conflicts", message: strings.Join(lines, "\n"), workspace: workspace.Root, merge: &workspace.Merge, paths: paths}
}

func requestMergeEdits(workspace *requestMergeWorkspace, choices []requestConflictChoice) (*cloud.RequestMergeOptions, map[string]*cloud.MergeFile, error) {
	options := &cloud.RequestMergeOptions{MergeID: workspace.Merge.MergeID, ExpectedUpdateNumber: workspace.Merge.UpdateNumber,
		ExpectedCurrentRevisionID: workspace.Merge.CurrentRevisionID, PackageRef: workspace.PackageRef, Resolutions: []cloud.MergeResolution{}}
	selected := map[string]string{}
	conflicts := map[string]cloud.MergeConflict{}
	for _, conflict := range workspace.Merge.Conflicts {
		conflicts[conflict.Path] = conflict
	}
	for _, choice := range choices {
		if _, ok := conflicts[choice.Path]; !ok || selected[choice.Path] != "" {
			return nil, nil, fmt.Errorf("%s is not an unresolved conflict", choice.Path)
		}
		selected[choice.Path] = choice.Choice
	}
	files, err := workspace.readLocalFiles()
	if err != nil {
		return nil, nil, err
	}
	results := map[string]*cloud.MergeFile{}
	unresolved := []cloud.MergeConflict{}
	for _, original := range workspace.Merge.Files {
		file := files[original.Path]
		conflict, hasConflict := conflicts[original.Path]
		choice := selected[original.Path]
		if hasConflict && choice == "" {
			if conflict.Kind != "content" || file == nil || file.Content == nil || containsMergeMarkers(*file.Content) {
				unresolved = append(unresolved, conflict)
				continue
			}
			choice = "local"
		}
		if hasConflict {
			resolution := cloud.MergeResolution{ConflictID: conflict.ID, Choice: choice}
			switch choice {
			case "current":
				file = original.Current
			case "proposed":
				file = original.Proposed
			case "local":
				if original.Kind == "skill_metadata" {
					return nil, nil, fmt.Errorf("%s is skill requirement metadata; choose current or proposed", original.Path)
				}
				if mergeFileBinary(original.Base) || mergeFileBinary(original.Current) || mergeFileBinary(original.Proposed) {
					return nil, nil, fmt.Errorf("%s is binary; resolve it with --resolve '%s=current' or --resolve '%s=proposed'", original.Path, original.Path, original.Path)
				}
				resolution.Choice = "merged"
				if file != nil {
					resolution.Content = file.Content
				}
			}
			options.Resolutions = append(options.Resolutions, resolution)
		}
		results[original.Path] = file
		if choice == "current" || choice == "proposed" {
			continue
		}
		if !sameMergeFile(original.Merged, file) || choice == "local" {
			if mergeFileBinary(file) || mergeFileBinary(original.Merged) {
				return nil, nil, fmt.Errorf("%s has custom binary edits; keep an exact source version and make additional binary edits in a later request update", original.Path)
			}
			edit := cloud.MergeFileEdit{Path: original.Path}
			if file != nil {
				edit.Content, edit.Mode = file.Content, file.Mode
			}
			options.Files = append(options.Files, edit)
		}
		delete(files, original.Path)
	}
	if len(unresolved) > 0 {
		return nil, nil, requestMergeConflictError(workspace, unresolved)
	}
	// New regular text resources inside an existing skill are allowed during resolution.
	for name, file := range files {
		if file == nil {
			continue
		}
		if _, exists := results[name]; exists {
			continue
		}
		if mergeFileBinary(file) {
			return nil, nil, fmt.Errorf("%s is a new binary file; add it in a later request update", name)
		}
		results[name] = file
		options.Files = append(options.Files, cloud.MergeFileEdit{Path: name, Content: file.Content, Mode: file.Mode})
	}
	sort.Slice(options.Files, func(i, j int) bool { return options.Files[i].Path < options.Files[j].Path })
	return options, results, nil
}

func containsMergeMarkers(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(line, "<<<<<<< Current deployment") || strings.HasPrefix(line, "||||||| Original") || strings.HasPrefix(line, ">>>>>>> Your proposed changes") {
			return true
		}
	}
	return false
}
func mergeFileBinary(file *cloud.MergeFile) bool { return file != nil && file.Content == nil }
func sameMergeFile(a, b *cloud.MergeFile) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, le := mergeFileBytes(a)
	right, re := mergeFileBytes(b)
	return le == nil && re == nil && string(left) == string(right) && a.Mode == b.Mode
}

// The merge response itself is pinned by Cloud. The local state records the
// originating request and candidate; it never grants permission to apply.
func saveMergedRequest(control *cloud.Client, request *cloud.ChangeRequestRecord, workspace *requestMergeWorkspace, choices []requestConflictChoice, message string, writer *savedPlanWriter, output string, jsonOut bool) error {
	options := workspace.Outgoing
	files := workspace.Result
	if options == nil {
		var err error
		options, files, err = requestMergeEdits(workspace, choices)
		if err != nil {
			return err
		}
		options.RevisionMessage = message
		workspace.Outgoing = options
		workspace.Result = files
		if err := workspace.save(); err != nil {
			return err
		}
	}
	updated, err := control.ResolveRequestMerge(*request, *options)
	if err != nil {
		var apiError *cloud.APIError
		if errors.As(err, &apiError) && apiError.StatusCode >= 400 && apiError.StatusCode < 500 && apiError.StatusCode != 409 {
			workspace.Outgoing = nil
			_ = workspace.save()
		}
		return err
	}
	if updated.ID != request.ID || updated.DeploymentID != request.DeploymentID || updated.UpdateNumber != workspace.Merge.UpdateNumber+1 || updated.PreparedPlanID == "" {
		return fmt.Errorf("Cloud returned an unexpected request update; inspect request %s; local files are preserved", request.ID)
	}
	// Persist the accepted result before changing local files. If synchronization
	// is interrupted, the same command finishes it without creating another update.
	workspace.Saved = updated
	workspace.Result = files
	if updated.Preview != nil {
		value := updated.Preview.ProposedSpec
		workspace.Result["SPEC.md"] = &cloud.MergeFile{Content: &value, Mode: "0644"}
	}
	if err := workspace.save(); err != nil {
		return fmt.Errorf("request %s was updated, but local recovery state could not be saved: %w; inspect the request before retrying", request.ID, err)
	}
	return finishMergedRequest(control, workspace, writer, output, jsonOut)
}
func finishMergedRequest(control *cloud.Client, workspace *requestMergeWorkspace, writer *savedPlanWriter, output string, jsonOut bool) error {
	if err := workspace.materialize(workspace.Result, false); err != nil {
		return fmt.Errorf("request %s was updated; local synchronization is pending: %w; rerun the same command after restoring the affected files", workspace.Saved.ID, err)
	}
	updated := workspace.Saved
	workspace.Merge = cloud.RequestMerge{RequestID: updated.ID, UpdateNumber: updated.UpdateNumber, PreparedPlanID: updated.PreparedPlanID}
	workspace.PackageRef = ""
	workspace.Materialized = false
	workspace.Outgoing = nil
	workspace.Result = nil
	workspace.Saved = nil
	workspace.Original = nil
	workspace.Written = nil
	if err := workspace.save(); err != nil {
		return err
	}
	return writeUpdatedRequestPlan(control, updated, writer, workspace.OrgID, output, jsonOut)
}

func loadRequestMergeWorkspace(root, specName, requestID string) (*requestMergeWorkspace, error) {
	filename := requestStatePath(requestID)
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	data, err := readSafeMergeFile(handle, filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result requestMergeWorkspace
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("invalid local request state: %w", err)
	}
	if result.Version != 1 || result.Root != root || result.SpecName != specName || result.Merge.RequestID != requestID || result.Merge.UpdateNumber < 1 {
		return nil, fmt.Errorf("local request state belongs to a different package or request")
	}
	return &result, nil
}
