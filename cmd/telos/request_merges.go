package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
)

const (
	requestMergeManifest  = "merge.json"
	maxMergePackageBytes  = 16 << 20
	maxMergeManifestBytes = 128 << 20
)

// The workspace pins the exact merge and account. Editing its resolution choices
// never authorizes application; Cloud checks the same identities again on save.
type requestMergeWorkspace struct {
	Version      int                     `json:"version"`
	Context      string                  `json:"context"`
	OrgID        string                  `json:"org_id"`
	APIEndpoint  string                  `json:"api_endpoint"`
	DeploymentID string                  `json:"deployment_id"`
	Merge        cloud.RequestMerge      `json:"merge"`
	Resolutions  []cloud.MergeResolution `json:"resolutions"`
}

type requestMergeError struct {
	code, message, workspace string
	merge                    *cloud.RequestMerge
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
			receipt["merge"] = mergeError.merge
		}
	}
	receipt["error"] = map[string]string{"code": code, "message": err.Error()}
	printJSON(receipt)
}

func runCloudRequestReconcile(requestID, directory, contextOverride string, jsonOut bool) error {
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("--reconcile requires a new directory; refusing to replace %s", directory)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return err
	}
	request, err := editableCloudRequest(control, requestID)
	if err != nil {
		return err
	}
	merge, err := control.PrepareRequestMerge(*request)
	if err != nil {
		return err
	}
	orgID, err := cloudPlanOrgID(control)
	if err != nil {
		return err
	}
	workspace := requestMergeWorkspace{
		Version: 1, Context: control.ContextName(), OrgID: orgID, APIEndpoint: control.Endpoint,
		DeploymentID: request.DeploymentID, Merge: *merge, Resolutions: []cloud.MergeResolution{},
	}
	for _, conflict := range merge.Conflicts {
		workspace.Resolutions = append(workspace.Resolutions, cloud.MergeResolution{ConflictID: conflict.ID})
	}
	if err := writeRequestMergeWorkspace(directory, workspace); err != nil {
		return err
	}
	if len(merge.Conflicts) != 0 {
		return &requestMergeError{
			code: "merge_conflicts", workspace: directory, merge: merge,
			message: fmt.Sprintf("%d conflicts saved in %s; edit merged files and choose each resolution in merge.json, then run telos plan --request %s --resolve %s", len(merge.Conflicts), directory, requestID, directory),
		}
	}
	if jsonOut {
		printJSON(map[string]any{"operation": "merge_prepared", "workspace": directory, "merge": merge})
	} else {
		printSummaryField(os.Stdout, "Workspace", directory)
		fmt.Fprintf(os.Stdout, "No text conflicts. Review merged files, then run telos plan --request %s --resolve %s\n", requestID, directory)
	}
	return nil
}

func validMergePath(name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return false
	}
	parts := strings.Split(name, "/")
	return name == "SPEC.md" || (len(parts) >= 3 && parts[0] == "skills") ||
		(len(parts) == 3 && parts[0] == ".telos" && parts[1] == "skills" && strings.HasSuffix(parts[2], ".json"))
}

func mergeFileBytes(file *cloud.MergeFile) ([]byte, error) {
	if (file.Content == nil) == (file.DataBase64 == nil) || (file.Mode != "0644" && file.Mode != "0755") {
		return nil, fmt.Errorf("invalid merge file encoding or mode")
	}
	if file.Content != nil {
		return []byte(*file.Content), nil
	}
	return base64.StdEncoding.DecodeString(*file.DataBase64)
}

func writeRequestMergeWorkspace(directory string, workspace requestMergeWorkspace) (err error) {
	if err := os.Mkdir(directory, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, tree := range []string{"base", "current", "proposed", "merged"} {
		if err := root.Mkdir(tree, 0o700); err != nil {
			return err
		}
	}
	for _, file := range workspace.Merge.Files {
		if !validMergePath(file.Path) {
			return fmt.Errorf("unsafe merge path %q", file.Path)
		}
		for _, tree := range []struct {
			name string
			file *cloud.MergeFile
		}{{"base", file.Base}, {"current", file.Current}, {"proposed", file.Proposed}, {"merged", file.Merged}} {
			if tree.file == nil {
				continue
			}
			data, err := mergeFileBytes(tree.file)
			if err != nil {
				return fmt.Errorf("%s: %w", file.Path, err)
			}
			name := path.Join(tree.name, file.Path)
			if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if tree.file.Mode == "0755" {
				mode = 0o700
			}
			output, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, writeErr := output.Write(data)
			closeErr := output.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return err
	}
	return root.WriteFile(requestMergeManifest, append(data, '\n'), 0o600)
}

func readRequestMergeWorkspace(root *os.Root) (*requestMergeWorkspace, error) {
	file, err := root.Open(requestMergeManifest)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMergeManifestBytes {
		return nil, fmt.Errorf("merge.json must be a regular file no larger than 128 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMergeManifestBytes+1))
	if err != nil || len(data) > maxMergeManifestBytes {
		return nil, fmt.Errorf("could not read merge.json within its 128 MiB limit")
	}
	var workspace requestMergeWorkspace
	if err := json.Unmarshal(data, &workspace); err != nil {
		return nil, fmt.Errorf("invalid merge.json: %w", err)
	}
	if workspace.Version != 1 || workspace.DeploymentID == "" || workspace.Merge.MergeID == "" || workspace.Merge.RequestID == "" || workspace.Merge.UpdateNumber < 1 {
		return nil, fmt.Errorf("invalid merge.json identity; prepare a new reconciliation workspace")
	}
	return &workspace, nil
}

func readMergeFile(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMergePackageBytes {
		return nil, fmt.Errorf("%s must be a regular file no larger than 16 MiB", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMergePackageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMergePackageBytes {
		return nil, fmt.Errorf("%s exceeds 16 MiB", name)
	}
	return data, nil
}

func requestMergeEdits(root *os.Root, workspace *requestMergeWorkspace) (*cloud.RequestMergeOptions, error) {
	options := &cloud.RequestMergeOptions{
		MergeID: workspace.Merge.MergeID, ExpectedUpdateNumber: workspace.Merge.UpdateNumber,
		ExpectedCurrentRevisionID: workspace.Merge.CurrentRevisionID, Resolutions: []cloud.MergeResolution{},
	}
	conflictByID := map[string]cloud.MergeConflict{}
	for _, conflict := range workspace.Merge.Conflicts {
		conflictByID[conflict.ID] = conflict
	}
	choices := map[string]string{}
	for _, resolution := range workspace.Resolutions {
		conflict, found := conflictByID[resolution.ConflictID]
		if !found || choices[conflict.Path] != "" {
			return nil, &requestMergeError{code: "invalid_resolution", message: "merge.json contains unknown or duplicate conflict resolutions"}
		}
		switch resolution.Choice {
		case "base", "current", "proposed", "merged":
			choices[conflict.Path] = resolution.Choice
		default:
			return nil, &requestMergeError{code: "merge_conflicts", message: "choose base, current, proposed, or merged for every conflict in merge.json; removing markers alone does not resolve a conflict"}
		}
		resolution.Content = nil
		options.Resolutions = append(options.Resolutions, resolution)
	}
	if len(options.Resolutions) != len(conflictByID) {
		return nil, &requestMergeError{code: "merge_conflicts", message: "merge.json must explicitly resolve every conflict"}
	}
	files := map[string]cloud.MergeFileEdit{}
	totalBytes := 0
	err := fs.WalkDir(root.FS(), "merged", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(name, "merged/")
		if !validMergePath(relative) || !entry.Type().IsRegular() {
			return fmt.Errorf("only regular package files are allowed in merged/: %s", relative)
		}
		if choice := choices[relative]; choice != "" && choice != "merged" {
			return nil
		}
		data, err := readMergeFile(root, name)
		if err != nil {
			return err
		}
		totalBytes += len(data)
		if totalBytes > maxMergePackageBytes {
			return fmt.Errorf("merged package exceeds 16 MiB")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := "0644"
		if info.Mode()&0o111 != 0 {
			mode = "0755"
		}
		// Unchanged binaries are preserved by Cloud; custom binary merges are
		// intentionally unsupported. Conflicts can choose an exact source side.
		for _, original := range workspace.Merge.Files {
			if original.Path == relative && original.Merged != nil {
				previous, err := mergeFileBytes(original.Merged)
				if err != nil {
					return err
				}
				if choices[relative] != "merged" && bytes.Equal(previous, data) && original.Merged.Mode == mode {
					return nil
				}
				break
			}
		}
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return &requestMergeError{code: "invalid_resolution", message: fmt.Sprintf("%s is binary; choose base, current, or proposed in merge.json", relative)}
		}
		content := string(data)
		files[relative] = cloud.MergeFileEdit{Path: relative, Content: &content, Mode: mode}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, original := range workspace.Merge.Files {
		if !validMergePath(original.Path) {
			return nil, fmt.Errorf("unsafe merge path %q", original.Path)
		}
		if choice := choices[original.Path]; choice != "" && choice != "merged" {
			continue
		}
		if _, err := root.Lstat(path.Join("merged", original.Path)); errors.Is(err, os.ErrNotExist) && original.Merged != nil {
			files[original.Path] = cloud.MergeFileEdit{Path: original.Path}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for i := range options.Resolutions {
		resolution := &options.Resolutions[i]
		if resolution.Choice == "merged" {
			resolution.Content = files[conflictByID[resolution.ConflictID].Path].Content
		}
	}
	for _, file := range files {
		options.Files = append(options.Files, file)
	}
	sort.Slice(options.Files, func(i, j int) bool { return options.Files[i].Path < options.Files[j].Path })
	return options, nil
}

func runCloudRequestResolve(requestID, directory, contextOverride, message, output string, jsonOut bool) error {
	message, err := normalizePlanMessage(message, false)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	workspace, err := readRequestMergeWorkspace(root)
	if err != nil {
		return err
	}
	if workspace.Merge.RequestID != requestID {
		return fmt.Errorf("workspace belongs to request %s, not %s", workspace.Merge.RequestID, requestID)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return err
	}
	if err := validateSavedDeploymentPlan(control, &savedDeploymentPlan{Context: workspace.Context, OrgID: workspace.OrgID, APIEndpoint: workspace.APIEndpoint}); err != nil {
		return err
	}
	options, err := requestMergeEdits(root, workspace)
	if err != nil {
		var mergeError *requestMergeError
		if errors.As(err, &mergeError) {
			return err
		}
		return &requestMergeError{code: "invalid_resolution", message: err.Error()}
	}
	options.RevisionMessage = message
	var writer *savedPlanWriter
	if output != "" {
		writer, err = prepareSavedPlanWriter(output)
		if err != nil {
			return err
		}
		defer writer.close()
	}
	request := cloud.ChangeRequestRecord{ID: requestID, DeploymentID: workspace.DeploymentID}
	updated, err := control.ResolveRequestMerge(request, *options)
	if err != nil {
		return err
	}
	if updated.ID != requestID || updated.DeploymentID != workspace.DeploymentID || updated.UpdateNumber != workspace.Merge.UpdateNumber+1 || updated.PreparedPlanID == "" {
		return fmt.Errorf("Cloud returned an unexpected merge result; inspect request %s; local files are preserved in %s", requestID, filepath.Clean(directory))
	}
	return writeUpdatedRequestPlan(control, updated, writer, workspace.OrgID, output, jsonOut)
}
