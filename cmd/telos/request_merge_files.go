package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"syscall"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
	"gopkg.in/yaml.v3"
)

const maxMergePackageBytes = 16 << 20
const maxMergeManifestBytes = 128 << 20

type requestMergeWorkspace struct {
	Outgoing     *cloud.RequestMergeOptions  `json:"outgoing,omitempty"`
	Version      int                         `json:"version"`
	Root         string                      `json:"root"`
	SpecName     string                      `json:"spec_name"`
	Context      string                      `json:"context"`
	OrgID        string                      `json:"org_id"`
	APIEndpoint  string                      `json:"api_endpoint"`
	DeploymentID string                      `json:"deployment_id"`
	PackageRef   string                      `json:"package_ref,omitempty"`
	Merge        cloud.RequestMerge          `json:"merge"`
	SkillPaths   map[string]string           `json:"skill_paths,omitempty"`
	Original     map[string]*cloud.MergeFile `json:"original,omitempty"`
	Written      map[string]*cloud.MergeFile `json:"written,omitempty"`
	Saved        *cloud.ChangeRequestRecord  `json:"saved,omitempty"`
	Result       map[string]*cloud.MergeFile `json:"result,omitempty"`
	Materialized bool                        `json:"materialized,omitempty"`
}

func requestStatePath(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return ".telos/request-" + hex.EncodeToString(sum[:16]) + ".json"
}
func requestRoot(specArg string) (string, string, error) {
	absolute, err := filepath.Abs(resolveSpecPath(specArg))
	if err != nil {
		return "", "", err
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(filepath.Join(root, filepath.Base(absolute)))
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("SPEC must be a regular file, not a symlink")
	}
	return root, filepath.Base(absolute), nil
}
func recordRequestWorkspace(control *cloud.Client, request *cloud.ChangeRequestRecord, specArg string) error {
	if request.Kind == "plan" || request.Mode == "preview" || request.UpdateNumber < 1 || strings.HasPrefix(specArg, "@") {
		return nil
	}
	root, name, err := requestRoot(specArg)
	if err != nil {
		return err
	}
	old, err := loadRequestMergeWorkspace(root, name, request.ID)
	if err != nil {
		return err
	}
	if old != nil && old.Merge.MergeID != "" {
		return fmt.Errorf("finish the local conflict resolution for request %s first", request.ID)
	}
	org, err := cloudPlanOrgID(control)
	if err != nil {
		return err
	}
	state := requestMergeWorkspace{Version: 1, Root: root, SpecName: name, Context: control.ContextName(), OrgID: org, APIEndpoint: control.Endpoint, DeploymentID: request.DeploymentID,
		Merge: cloud.RequestMerge{RequestID: request.ID, UpdateNumber: request.UpdateNumber, PreparedPlanID: request.PreparedPlanID}}
	return state.save()
}
func (w *requestMergeWorkspace) save() error {
	root, err := os.OpenRoot(w.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkMergeLocalPath(root, ".telos"); err != nil {
		return err
	}
	if err := root.MkdirAll(".telos", 0o700); err != nil {
		return err
	}
	name := requestStatePath(w.Merge.RequestID)
	if err := checkMergeLocalPath(root, name); err != nil {
		return err
	}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxMergeManifestBytes {
		return fmt.Errorf("local merge recovery state exceeds 128 MiB; no request was saved")
	}
	temp := ".telos/.request-state-" + rand.Text()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return root.Rename(temp, name)
}
func lockRequestWorkspace(directory string) (func(), error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkMergeLocalPath(root, ".telos"); err != nil {
		return nil, err
	}
	if err := root.MkdirAll(".telos", 0o700); err != nil {
		return nil, err
	}
	name := ".telos/request-planning.lock"
	if err := checkMergeLocalPath(root, name); err != nil {
		return nil, err
	}
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another request plan is using this package directory")
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
func checkOtherRequestMerges(directory, requestID string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".telos")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "request-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readSafeMergeFile(root, ".telos/"+entry.Name())
		if err != nil {
			return err
		}
		var state requestMergeWorkspace
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("invalid local request state %s", entry.Name())
		}
		if state.Merge.RequestID != requestID && state.Merge.MergeID != "" {
			return fmt.Errorf("finish resolving request %s in this directory before planning another request; your files are preserved", state.Merge.RequestID)
		}
	}
	return nil
}

func validMergePath(name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return false
	}
	parts := strings.Split(name, "/")
	return name == "SPEC.md" || len(parts) >= 3 && parts[0] == "skills" || len(parts) == 3 && parts[0] == ".telos" && parts[1] == "skills" && strings.HasSuffix(parts[2], ".json")
}
func safeLocalName(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.Contains(name, "\\") && name != ".telos" && !strings.HasPrefix(name, ".telos/")
}
func checkMergeLocalPath(root *os.Root, name string) error {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return fmt.Errorf("unsafe local package path %q", name)
	}
	prefix := ""
	for _, part := range strings.Split(name, "/") {
		prefix = path.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in local package path %s", prefix)
		}
		if prefix != name && !info.IsDir() {
			return fmt.Errorf("local package path collides with file %s", prefix)
		}
	}
	return nil
}
func readSafeMergeFile(root *os.Root, name string) ([]byte, error) {
	if err := checkMergeLocalPath(root, name); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMergeManifestBytes {
		return nil, fmt.Errorf("%s must be a regular file under 128 MiB", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMergeManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMergeManifestBytes {
		return nil, fmt.Errorf("%s exceeds 128 MiB", name)
	}
	return data, nil
}
func readLocalMergeFile(root *os.Root, name string) (*cloud.MergeFile, error) {
	data, err := readSafeMergeFile(root, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxMergePackageBytes {
		return nil, fmt.Errorf("%s exceeds 16 MiB", name)
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	file := &cloud.MergeFile{Mode: "0644"}
	if info.Mode()&0o111 != 0 {
		file.Mode = "0755"
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		value := string(data)
		file.Content = &value
	} else {
		value := base64.StdEncoding.EncodeToString(data)
		file.DataBase64 = &value
	}
	return file, nil
}
func mergeFileBytes(file *cloud.MergeFile) ([]byte, error) {
	if file == nil {
		return nil, nil
	}
	if (file.Content == nil) == (file.DataBase64 == nil) || (file.Mode != "0644" && file.Mode != "0755") {
		return nil, fmt.Errorf("invalid merge file encoding or mode")
	}
	if file.Content != nil {
		return []byte(*file.Content), nil
	}
	return base64.StdEncoding.DecodeString(*file.DataBase64)
}
func (w *requestMergeWorkspace) localPath(name string) string {
	if name == "SPEC.md" {
		return w.SpecName
	}
	parts := strings.SplitN(name, "/", 3)
	if len(parts) == 3 && parts[0] == "skills" {
		return path.Join(w.SkillPaths[parts[1]], parts[2])
	}
	return name
}
func (w *requestMergeWorkspace) snapshot() (map[string]*cloud.MergeFile, error) {
	root, err := os.OpenRoot(w.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := map[string]*cloud.MergeFile{}
	result[w.SpecName], err = readLocalMergeFile(root, w.SpecName)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, directory := range w.SkillPaths {
		if !safeLocalName(directory) {
			return nil, fmt.Errorf("unsafe local skill path %q", directory)
		}
		if err := checkMergeLocalPath(root, directory); err != nil {
			return nil, err
		}
		err := fs.WalkDir(root.FS(), directory, func(name string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) && name == directory {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("only regular skill files are supported: %s", name)
			}
			file, err := readLocalMergeFile(root, name)
			if err != nil {
				return err
			}
			data, _ := mergeFileBytes(file)
			total += len(data)
			if total > maxMergePackageBytes {
				return fmt.Errorf("local package exceeds 16 MiB")
			}
			result[name] = file
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func sameLocalSnapshot(a, b map[string]*cloud.MergeFile) bool {
	for name, file := range a {
		if !sameMergeFile(file, b[name]) {
			return false
		}
	}
	for name, file := range b {
		if !sameMergeFile(file, a[name]) {
			return false
		}
	}
	return true
}
func (w *requestMergeWorkspace) validateMergeFiles() error {
	seen := map[string]bool{}
	root, err := os.OpenRoot(w.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range w.Merge.Files {
		if !validMergePath(file.Path) || seen[file.Path] {
			return fmt.Errorf("unsafe or duplicate merge path %q", file.Path)
		}
		seen[file.Path] = true
		for _, content := range []*cloud.MergeFile{file.Base, file.Current, file.Proposed, file.Merged} {
			if _, err := mergeFileBytes(content); err != nil {
				return err
			}
		}
		if strings.HasPrefix(file.Path, "skills/") {
			name := strings.Split(file.Path, "/")[1]
			if _, found := w.SkillPaths[name]; !found {
				directory := "skills/" + name
				if err := checkMergeLocalPath(root, directory); err != nil {
					return err
				}
				if _, err := root.Lstat(directory); err == nil {
					return fmt.Errorf("refusing to overwrite unrelated local skill directory %s", directory)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				w.SkillPaths[name] = directory
			}
		}
	}
	for _, conflict := range w.Merge.Conflicts {
		if !seen[conflict.Path] {
			return fmt.Errorf("Cloud returned conflict for unknown path %s", conflict.Path)
		}
	}
	return nil
}

// Only the skills field is rewritten for local path resolution. Markdown body,
// other frontmatter, and their whitespace are preserved byte-for-byte.
func rewriteMergeSpec(content string, paths map[string]string, required map[string]bool) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", fmt.Errorf("merged SPEC.md has no frontmatter")
	}
	end := 1
	for end < len(lines) && strings.TrimSpace(lines[end]) != "---" {
		end++
	}
	if end == len(lines) {
		return "", fmt.Errorf("merged SPEC.md has no closing frontmatter")
	}
	header := strings.Join(lines[1:end], "")
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(header), &document); err != nil {
		return "", fmt.Errorf("resolve SPEC.md frontmatter conflicts first: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("SPEC.md frontmatter must be a mapping")
	}
	mapping := document.Content[0]
	if mergeHeaderAliases(mapping) {
		return "", fmt.Errorf("anchored or aliased SPEC frontmatter cannot be synchronized safely; use explicit values")
	}
	if mapping.Style&yaml.FlowStyle != 0 {
		return "", fmt.Errorf("flow-style SPEC frontmatter cannot be synchronized safely; expand it to one key per line before planning")
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Value == "<<" {
			return "", fmt.Errorf("inherited SPEC frontmatter cannot be synchronized safely; use explicit frontmatter fields")
		}
		if i+2 < len(mapping.Content) && mapping.Content[i+2].Line == key.Line {
			return "", fmt.Errorf("SPEC frontmatter keys must be on separate lines")
		}
	}
	start, finish := -1, end
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Value != "skills" {
			continue
		}
		start = key.Line
		if index+2 < len(mapping.Content) {
			finish = mapping.Content[index+2].Line
		}
		break
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	newline := "\n"
	if strings.HasSuffix(lines[0], "\r\n") {
		newline = "\r\n"
	}
	replacement := ""
	if len(names) > 0 {
		replacement = "skills:" + newline
		for _, name := range names {
			ref := "./" + paths[name]
			if required[name] {
				ref += "*"
			}
			encoded, _ := json.Marshal(ref)
			replacement += "  - " + string(encoded) + newline
		}
	}
	if start < 0 {
		if replacement == "" {
			return content, nil
		}
		return strings.Join(lines[:end], "") + replacement + strings.Join(lines[end:], ""), nil
	}
	if len(names) == 0 {
		replacement = "skills: []" + newline
	}
	return strings.Join(lines[:start], "") + replacement + strings.Join(lines[finish:], ""), nil
}
func mergeHeaderAliases(node *yaml.Node) bool {
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if mergeHeaderAliases(child) {
			return true
		}
	}
	return false
}

func mergeRequirements(files map[string]*cloud.MergeFile) (map[string]bool, error) {
	result := map[string]bool{}
	for name, file := range files {
		if !strings.HasPrefix(name, ".telos/skills/") || file == nil {
			continue
		}
		data, err := mergeFileBytes(file)
		if err != nil {
			return nil, err
		}
		var metadata struct {
			Required *bool `json:"required"`
		}
		if err := json.Unmarshal(data, &metadata); err != nil || metadata.Required == nil {
			return nil, fmt.Errorf("invalid skill requirement metadata %s", name)
		}
		result[strings.TrimSuffix(strings.TrimPrefix(name, ".telos/skills/"), ".json")] = *metadata.Required
	}
	return result, nil
}
func (w *requestMergeWorkspace) localFiles(files map[string]*cloud.MergeFile) (map[string]*cloud.MergeFile, error) {
	result := map[string]*cloud.MergeFile{}
	required, err := mergeRequirements(files)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for name := range required {
		paths[name] = w.SkillPaths[name]
	}
	for name, file := range files {
		if strings.HasPrefix(name, ".telos/") {
			continue
		}
		local := w.localPath(name)
		if !safeLocalName(local) {
			return nil, fmt.Errorf("unsafe local merge destination %q", local)
		}
		if name == "SPEC.md" && file != nil && file.Content != nil {
			content, err := rewriteMergeSpec(*file.Content, paths, required)
			if err != nil {
				// Frontmatter markers must remain editable; skill paths are
				// normalized after the user resolves that text conflict.
				if !containsMergeMarkers(*file.Content) {
					return nil, err
				}
				content = *file.Content
			}
			copy := *file
			copy.Content = &content
			file = &copy
		}
		result[local] = file
	}
	return result, nil
}
func (w *requestMergeWorkspace) materialize(files map[string]*cloud.MergeFile, initial bool) error {
	targets, err := w.localFiles(files)
	if err != nil {
		return err
	}
	collisions := map[string]bool{}
	for name, file := range targets {
		if file == nil {
			continue
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if targets[parent] != nil {
				collisions[name] = true
				collisions[parent] = true
			}
		}
	}
	if len(collisions) > 0 {
		if !initial {
			return fmt.Errorf("resolved package selects both a file and its child path; resolve the conflicting paths consistently")
		}
		// Keep impossible file/directory conflicts untouched until explicit
		// choices produce one valid tree; all conflict details still print.
		for name := range collisions {
			delete(targets, name)
		}
	}
	actual, err := w.snapshot()
	if err != nil {
		return err
	}
	expected := w.Written
	if initial {
		expected = w.Original
	}
	// A retry can encounter files already written by a previous interrupted
	// synchronization. Every other local edit is preserved and blocks writes.
	for name, file := range actual {
		if !sameMergeFile(file, expected[name]) && !sameMergeFile(file, targets[name]) {
			return fmt.Errorf("%s changed locally while planning; no further files were overwritten", name)
		}
	}
	for name, file := range expected {
		if !sameMergeFile(actual[name], file) && !sameMergeFile(actual[name], targets[name]) {
			return fmt.Errorf("%s changed locally while planning", name)
		}
	}
	root, err := os.OpenRoot(w.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	names := make([]string, 0, len(targets))
	for name := range targets {
		if err := checkMergeWritePath(root, name, targets); err != nil {
			return err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	// Remove only tracked files selected for deletion, before creating parents
	// for a file-to-directory replacement. No recursive deletion is used.
	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		if targets[name] != nil {
			continue
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	for _, name := range names {
		file := targets[name]
		if sameMergeFile(actual[name], file) {
			continue
		}
		if file == nil {
			continue
		}
		if info, err := root.Lstat(name); err == nil && info.IsDir() {
			if err := removeEmptyMergeDirectory(root, name); err != nil {
				return fmt.Errorf("refusing to replace nonempty directory %s: %w", name, err)
			}
		}
		if err := checkMergeLocalPath(root, name); err != nil {
			return err
		}
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		data, err := mergeFileBytes(file)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info, err := root.Lstat(name); err == nil && info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		mode &^= 0o111
		if file.Mode == "0755" {
			mode |= (mode & 0o444) >> 2
		}
		// Atomic replacement never truncates a symlink/hardlink target.
		if err := checkMergeLocalPath(root, ".telos"); err != nil {
			return err
		}
		if err := root.MkdirAll(".telos", 0o700); err != nil {
			return err
		}
		temp := ".telos/.request-file-" + rand.Text()
		output, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, writeErr := output.Write(data)
		closeErr := output.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			root.Remove(temp)
			return err
		}
		if err := root.Rename(temp, name); err != nil {
			root.Remove(temp)
			return err
		}
	}
	w.Written, err = w.snapshot()
	return err
}
func checkMergeWritePath(root *os.Root, name string, targets map[string]*cloud.MergeFile) error {
	if !safeLocalName(name) {
		return fmt.Errorf("unsafe merge write path %q", name)
	}
	prefix := ""
	for _, part := range strings.Split(name, "/") {
		prefix = path.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in local merge path %s", prefix)
		}
		if prefix != name && !info.IsDir() {
			target, listed := targets[prefix]
			if !listed || target != nil {
				return fmt.Errorf("local merge destination is blocked by file %s", prefix)
			}
		}
	}
	return nil
}
func removeEmptyMergeDirectory(root *os.Root, name string) error {
	directories := []string{}
	if err := fs.WalkDir(root.FS(), name, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return fmt.Errorf("unrelated file %s remains", current)
		}
		directories = append(directories, current)
		return nil
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := root.Remove(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

func (w *requestMergeWorkspace) readLocalFiles() (map[string]*cloud.MergeFile, error) {
	snapshot, err := w.snapshot()
	if err != nil {
		return nil, err
	}
	result := map[string]*cloud.MergeFile{}
	for _, original := range w.Merge.Files {
		if original.Kind == "skill_metadata" || strings.HasPrefix(original.Path, ".telos/skills/") {
			result[original.Path] = original.Merged
			continue
		}
		result[original.Path] = snapshot[w.localPath(original.Path)]
	}
	for skill, directory := range w.SkillPaths {
		for name, file := range snapshot {
			if strings.HasPrefix(name, directory+"/") {
				result["skills/"+skill+strings.TrimPrefix(name, directory)] = file
			}
		}
	}
	if file := result["SPEC.md"]; file != nil && file.Content != nil && !containsMergeMarkers(*file.Content) {
		required, err := localSpecRequirements(*file.Content, w.SkillPaths)
		if err != nil {
			return nil, err
		}
		for name := range w.SkillPaths {
			metadataPath := ".telos/skills/" + name + ".json"
			if flag, included := required[name]; included {
				value := fmt.Sprintf("{\"required\":%t}\n", flag)
				result[metadataPath] = &cloud.MergeFile{Content: &value, Mode: "0644"}
			} else {
				result[metadataPath] = nil
				for filename := range result {
					if strings.HasPrefix(filename, "skills/"+name+"/") {
						result[filename] = nil
					}
				}
			}
		}
		paths := map[string]string{}
		for name := range required {
			paths[name] = "skills/" + name
		}
		content, err := rewriteMergeSpec(*file.Content, paths, required)
		if err != nil {
			return nil, err
		}
		// Local paths are only an editing convenience. If the author did not
		// edit frontmatter, retain Cloud's exact header instead of introducing
		// path/format changes alongside a body-only conflict resolution.
		originals := map[string]*cloud.MergeFile{}
		for _, original := range w.Merge.Files {
			originals[original.Path] = original.Merged
		}
		localized, localErr := w.localFiles(originals)
		if localErr == nil && localized[w.SpecName] != nil && localized[w.SpecName].Content != nil && originals["SPEC.md"] != nil && originals["SPEC.md"].Content != nil {
			editedHeader, editedBody, editedOK := mergeSpecParts(*file.Content)
			localHeader, _, localOK := mergeSpecParts(*localized[w.SpecName].Content)
			cloudHeader, _, cloudOK := mergeSpecParts(*originals["SPEC.md"].Content)
			if editedOK && localOK && cloudOK && editedHeader == localHeader {
				content = cloudHeader + editedBody
			}
		}
		copy := *file
		copy.Content = &content
		result["SPEC.md"] = &copy
	}
	w.Written = snapshot
	return result, nil
}

func mergeSpecParts(content string) (header, body string, ok bool) {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}
	offset := len(lines[0])
	for _, line := range lines[1:] {
		offset += len(line)
		if strings.TrimSpace(line) == "---" {
			return content[:offset], content[offset:], true
		}
	}
	return "", "", false
}

func localSpecRequirements(content string, paths map[string]string) (map[string]bool, error) {
	header, _, ok := spec.ParseFrontmatter(content)
	if !ok {
		return nil, fmt.Errorf("resolve SPEC.md frontmatter conflicts first")
	}
	values := []string{}
	switch raw := header["skills"].(type) {
	case nil:
	case string:
		values = append(values, raw)
	case []any:
		for _, value := range raw {
			ref, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("SPEC.md skills must contain text references")
			}
			values = append(values, ref)
		}
	default:
		return nil, fmt.Errorf("SPEC.md skills must contain text references")
	}
	required := map[string]bool{}
	for _, value := range values {
		starred := strings.HasSuffix(value, "*")
		reference := strings.TrimSuffix(value, "*")
		name := ""
		if registry, ok := spec.ParseRegistrySkillRef(reference); ok {
			if directory, known := paths[registry.Name]; known {
				return nil, fmt.Errorf("resolve %s using the local skill path ./%s; change its Registry reference in a later plan so the selected version is fetched and reviewed", reference, directory)
			}
		} else {
			clean := strings.TrimPrefix(reference, "./")
			for candidate, directory := range paths {
				if clean == directory || clean == "skills/"+candidate {
					name = candidate
					break
				}
			}
		}
		if name == "" {
			return nil, fmt.Errorf("skill reference %q is not part of this merge; finish resolving the existing request before adding a new skill", reference)
		}
		if _, duplicate := required[name]; duplicate {
			return nil, fmt.Errorf("duplicate resolved skill %s", name)
		}
		required[name] = starred
	}
	return required, nil
}

func stageRequestMergePackage(control *cloud.Client, contextOverride string, w *requestMergeWorkspace) (*cloud.PackageVersionRecord, error) {
	var record *cloud.PackageVersionRecord
	specPath := filepath.Join(w.Root, w.SpecName)
	err := withPlanRegistrySkills(specPath, contextOverride, func() error {
		pkg, err := packageRequestSpec(specPath, contextOverride)
		if err != nil {
			return err
		}
		w.SkillPaths = map[string]string{}
		for _, skill := range pkg.compiled.Skills {
			directory, err := filepath.Rel(w.Root, skill.Path)
			if err != nil {
				return err
			}
			directory = filepath.ToSlash(directory)
			if !safeLocalName(directory) {
				directory = "skills/" + skill.Name
				if _, err := os.Lstat(filepath.Join(w.Root, filepath.FromSlash(directory))); err == nil {
					return fmt.Errorf("refusing to overwrite unrelated skill directory %s", directory)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			for _, existing := range w.SkillPaths {
				if directory == existing || strings.HasPrefix(directory, existing+"/") || strings.HasPrefix(existing, directory+"/") {
					return fmt.Errorf("overlapping skill directories cannot be reconciled safely")
				}
			}
			w.SkillPaths[skill.Name] = directory
		}
		w.Original, err = w.snapshot()
		if err != nil {
			return err
		}
		// Recompile instead of rebuilding cached metadata: a changed skills
		// list can otherwise be rewritten back to the old list with the same digest.
		checked, err := packageRequestSpec(specPath, contextOverride)
		if err != nil {
			return err
		}
		if checked.digest != pkg.digest || !sameCompiledSkillSources(pkg.compiled, checked.compiled) {
			return fmt.Errorf("local files changed while compiling; rerun plan; no local files or request were changed")
		}
		record, err = pushSpecPackage(control.PlanArtifactClient(), pkg, "")
		if err != nil {
			return err
		}
		after, err := w.snapshot()
		if err != nil {
			return err
		}
		if !sameLocalSnapshot(w.Original, after) {
			return fmt.Errorf("local files changed while uploading; rerun plan; no local files or request were changed")
		}
		return nil
	})
	return record, err
}

func sameCompiledSkillSources(a, b *spec.CompiledEnvironment) bool {
	if len(a.Skills) != len(b.Skills) {
		return false
	}
	for i, skill := range a.Skills {
		if skill.Name != b.Skills[i].Name || skill.Path != b.Skills[i].Path || skill.SourceRef != b.Skills[i].SourceRef {
			return false
		}
	}
	return true
}

// A downloaded manifest resolves exact Registry references locally, but it must
// not add skills the author removed from SPEC.md while editing a request.
func packageRequestSpec(specPath, contextOverride string) (*specPackage, error) {
	pkg, err := packageSpec(specPath, contextOverride)
	if err != nil {
		return nil, err
	}
	declared, err := spec.ResolveSkillsFromPaths(pkg.compiled.Environment.SkillPaths)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, skill := range declared {
		selected[skill.Path] = true
	}
	skills := pkg.compiled.Skills[:0]
	for _, skill := range pkg.compiled.Skills {
		if selected[skill.Path] {
			skills = append(skills, skill)
		}
	}
	pkg.compiled.Skills = skills
	required := pkg.compiled.RequiredVerifierSkills[:0]
	for _, skill := range pkg.compiled.RequiredVerifierSkills {
		if selected[skill.Path] {
			required = append(required, skill)
		}
	}
	pkg.compiled.RequiredVerifierSkills = required
	rebuilt, err := spec.BuildApplyPackage(pkg.compiled)
	if err != nil {
		return nil, err
	}
	pkg.digest, pkg.bytes = rebuilt.Digest, rebuilt.Bytes
	return pkg, nil
}
func stageRequestUpdatePackage(control *cloud.Client, specPath, contextOverride string) (*cloud.PackageVersionRecord, error) {
	var record *cloud.PackageVersionRecord
	err := withPlanRegistrySkills(specPath, contextOverride, func() error {
		pkg, err := packageRequestSpec(specPath, contextOverride)
		if err != nil {
			return err
		}
		record, err = pushSpecPackage(control.PlanArtifactClient(), pkg, "")
		return err
	})
	return record, err
}
