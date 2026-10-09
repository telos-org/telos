package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type revisionFileDiff struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	OldMode string `json:"old_mode,omitempty"`
	NewMode string `json:"new_mode,omitempty"`
	Patch   string `json:"patch,omitempty"`
	Notice  string `json:"notice,omitempty"`
}

type revisionSkillChange struct {
	Name   string                      `json:"name"`
	Before *spec.ApplyPackageSkillLock `json:"before"`
	After  *spec.ApplyPackageSkillLock `json:"after"`
}

type revisionDiffResult struct {
	SessionID         string                `json:"session_id"`
	Context           string                `json:"context"`
	CurrentRevisionID string                `json:"current_revision_id"`
	From              *cloud.Revision       `json:"from"`
	To                *cloud.Revision       `json:"to"`
	Files             []revisionFileDiff    `json:"files"`
	Skills            []revisionSkillChange `json:"skills"`
}

func cmdRevisionDiff(args []string) {
	fs := newCommandFlagSet("diff", "telos diff SESSION FROM [TO] [flags]")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	if fs.NArg() != 2 && fs.NArg() != 3 {
		fmt.Fprintln(os.Stderr, "error: expected SESSION, FROM, and optional TO revision")
		fs.Usage()
		os.Exit(2)
	}
	for _, selector := range fs.Args()[1:] {
		if _, err := parseRevisionSelector(selector); err != nil {
			exitWithError(err)
		}
	}
	control, session, _, err := revisionCommandTarget(fs, *contextValue)
	if err != nil {
		exitWithError(err)
	}
	ctx := context.Background()
	page, err := control.ListSessionRevisions(ctx, session.ID, revisionPageSize, 0)
	if err != nil {
		exitWithError(err)
	}
	toSelector := page.CurrentRevisionID
	if fs.NArg() == 3 {
		toSelector = fs.Arg(2)
	}
	from, err := resolveRevision(ctx, control, session.ID, fs.Arg(1), page)
	if err != nil {
		exitWithError(err)
	}
	to, err := resolveRevision(ctx, control, session.ID, toSelector, page)
	if err != nil {
		exitWithError(err)
	}
	result, err := compareRevisions(ctx, control, session.ID, from, to)
	if err != nil {
		exitWithError(err)
	}
	result.Context = control.ContextName()
	result.CurrentRevisionID = page.CurrentRevisionID
	if *jsonOut {
		printJSON(result)
		return
	}
	printRevisionField(os.Stdout, "Goal", session.Name)
	printRevisionField(os.Stdout, "Session", session.ID)
	printRevisionField(os.Stdout, "Context", control.ContextName())
	printRevisionField(os.Stdout, "From", fmt.Sprintf("revision %d (%s)", from.Sequence, from.ID))
	toLabel := fmt.Sprintf("revision %d (%s)", to.Sequence, to.ID)
	if to.ID == page.CurrentRevisionID {
		toLabel += " (current when comparison started)"
	}
	printRevisionField(os.Stdout, "To", toLabel)
	for _, change := range result.Files {
		fmt.Fprintln(os.Stdout)
		if change.OldMode != change.NewMode {
			fmt.Printf("%s: mode %s -> %s\n", revisionText(change.Path), orDash(change.OldMode), orDash(change.NewMode))
		}
		if change.Notice != "" {
			fmt.Printf("%s: %s (%s)\n", revisionText(change.Path), change.Status, change.Notice)
		}
		fmt.Print(revisionPatchText(change.Patch))
	}
	for _, change := range result.Skills {
		fmt.Printf("\nSkill dependency %s:\n  From %s\n  To   %s\n", revisionText(change.Name), skillLockLabel(change.Before), skillLockLabel(change.After))
	}
	if len(result.Files) == 0 && len(result.Skills) == 0 {
		fmt.Println("\nNo package changes.")
	}
}

func compareRevisions(ctx context.Context, control *cloud.Client, sessionID string, from, to *cloud.Revision) (*revisionDiffResult, error) {
	result := &revisionDiffResult{SessionID: sessionID, From: from, To: to, Files: []revisionFileDiff{}, Skills: []revisionSkillChange{}}
	if from.PackageDigest == to.PackageDigest && from.PackageDigest != "" {
		return result, nil
	}
	oldFiles, oldManifest, err := revisionPackageFiles(ctx, control, sessionID, from)
	if err != nil {
		return nil, fmt.Errorf("read revision %d: %w", from.Sequence, err)
	}
	newFiles, newManifest, err := revisionPackageFiles(ctx, control, sessionID, to)
	if err != nil {
		return nil, fmt.Errorf("read revision %d: %w", to.Sequence, err)
	}
	paths := map[string]bool{}
	for path := range oldFiles {
		paths[path] = true
	}
	for path := range newFiles {
		paths[path] = true
	}
	for _, path := range sortedRevisionKeys(paths) {
		oldFile, hadOld := oldFiles[path]
		newFile, hasNew := newFiles[path]
		if hadOld && hasNew && oldFile.Mode == newFile.Mode && bytes.Equal(oldFile.Data, newFile.Data) {
			continue
		}
		change := revisionFileDiff{Path: path, Status: "modified"}
		oldName := fmt.Sprintf("revision-%d/%s", from.Sequence, path)
		newName := fmt.Sprintf("revision-%d/%s", to.Sequence, path)
		if hadOld {
			change.OldMode = fmt.Sprintf("%04o", oldFile.Mode)
		} else {
			change.Status, oldName = "added", "/dev/null"
		}
		if hasNew {
			change.NewMode = fmt.Sprintf("%04o", newFile.Mode)
		} else {
			change.Status, newName = "deleted", "/dev/null"
		}
		if !bytes.Equal(oldFile.Data, newFile.Data) {
			switch {
			case !utf8.Valid(oldFile.Data) || !utf8.Valid(newFile.Data) || bytes.ContainsRune(oldFile.Data, 0) || bytes.ContainsRune(newFile.Data, 0):
				change.Notice = "binary content differs"
			case len(oldFile.Data)+len(newFile.Data) > 2*1024*1024 || bytes.Count(oldFile.Data, []byte("\n"))+bytes.Count(newFile.Data, []byte("\n")) > 10000:
				change.Notice = "content differs; too large for a line diff"
			default:
				change.Patch = string(internaldiff.Diff(oldName, oldFile.Data, newName, newFile.Data))
			}
		}
		result.Files = append(result.Files, change)
	}
	names := map[string]bool{}
	for name := range oldManifest.Skills {
		names[name] = true
	}
	for name := range newManifest.Skills {
		names[name] = true
	}
	for _, name := range sortedRevisionKeys(names) {
		oldLock, oldOK := revisionSkillLock(oldManifest, name)
		newLock, newOK := revisionSkillLock(newManifest, name)
		if oldOK == newOK && oldLock == newLock {
			continue
		}
		change := revisionSkillChange{Name: name}
		if oldOK {
			change.Before = &oldLock
		}
		if newOK {
			change.After = &newLock
		}
		result.Skills = append(result.Skills, change)
	}
	return result, nil
}

func revisionPackageFiles(ctx context.Context, control *cloud.Client, sessionID string, revision *cloud.Revision) (map[string]spec.ApplyPackageFileContents, *spec.ApplyPackageManifest, error) {
	data, err := control.DownloadRevisionBundle(ctx, sessionID, revision.ID)
	if err != nil {
		return nil, nil, err
	}
	_, manifest, err := verifiedPackageContents(&pulledPackage{
		reference: packageReference{ref: revision.PackageRef}, digest: revision.PackageDigest, data: data,
	})
	if err != nil {
		return nil, nil, err
	}
	hydrated, _, err := spec.HydrateApplyPackage(data, func(request spec.ApplyPackageSkillFetchRequest) ([]byte, error) {
		ref, ok := spec.ParseRegistrySkillRef(request.Ref)
		if !ok || ref.Version == "" {
			return nil, fmt.Errorf("invalid historical skill ref %q", request.Ref)
		}
		return control.DownloadRevisionSkillBundle(ctx, sessionID, revision.ID, ref.Scope, ref.Name, ref.Version, request.Digest)
	})
	if err != nil {
		return nil, nil, err
	}
	files, err := spec.ApplyPackageFiles(hydrated)
	delete(files, "manifest.json")
	return files, manifest, err
}

func revisionSkillLock(manifest *spec.ApplyPackageManifest, name string) (spec.ApplyPackageSkillLock, bool) {
	lock, ok := manifest.Skills[name]
	if lock.Ref == "" {
		lock.Ref = manifest.SkillProvenance[name].Ref
	}
	return lock, ok
}

func sortedRevisionKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func skillLockLabel(lock *spec.ApplyPackageSkillLock) string {
	if lock == nil {
		return "(absent)"
	}
	return fmt.Sprintf("%s %s required=%t", orDash(revisionText(lock.Ref)), revisionText(lock.Digest), lock.Starred)
}

func revisionPatchText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return ' '
		}
		return r
	}, value)
}
