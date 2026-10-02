package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestRequestConflictTextPreservesCleanChangesAndLineEndings(t *testing.T) {
	source := "Keep deployed audit log.\n<<<<<<< Current deployment\ndeployed one\n||||||| Original\nbase one\n=======\nproposed one\n>>>>>>> Your proposed changes\nKeep proposed CSV export.\n<<<<<<< Current deployment\ndeployed two\n=======\nproposed two\n>>>>>>> Your proposed changes"
	expected := "Keep deployed audit log.\n<<<<<<< Proposed version (current change)\nproposed one\n=======\ndeployed one\n>>>>>>> Deployed version (incoming change)\nKeep proposed CSV export.\n<<<<<<< Proposed version (current change)\nproposed two\n=======\ndeployed two\n>>>>>>> Deployed version (incoming change)"
	for _, newline := range []string{"\n", "\r\n"} {
		for _, ending := range []string{"", newline} {
			input := strings.ReplaceAll(source, "\n", newline) + ending
			want := strings.ReplaceAll(expected, "\n", newline) + ending
			got, err := requestConflictText(input)
			if err != nil || got != want {
				t.Fatalf("newline=%q ending=%q got=%q want=%q err=%v", newline, ending, got, want, err)
			}
		}
	}
}

func TestRequestConflictTextRejectsMalformedBlocks(t *testing.T) {
	for _, value := range []string{
		"<<<<<<< Current deployment\nincomplete\n",
		"<<<<<<< Current deployment\n>>>>>>> Your proposed changes\n",
		"<<<<<<< Current deployment\n<<<<<<< Current deployment\n=======\n>>>>>>> Your proposed changes\n",
		"<<<<<<< Current deployment\n=======\n=======\n>>>>>>> Your proposed changes\n",
		"<<<<<<< Current deployment\n=======\n||||||| Original\n>>>>>>> Your proposed changes\n",
		"<<<<<<< Current deployment\n||||||| Original\n||||||| Original\n=======\n>>>>>>> Your proposed changes\n",
		"<<<<<<< Unexpected side\n=======\n>>>>>>> Your proposed changes\n",
		"<<<<<<< Current deployment\n=======\n>>>>>>> Unexpected side\n",
		"||||||| Original\n",
		">>>>>>> Your proposed changes\n",
	} {
		if _, err := requestConflictText(value); err == nil {
			t.Errorf("accepted malformed conflict: %q", value)
		}
	}
}

func TestRequestConflictTextPreservesMarkdownHeadingsWithinSides(t *testing.T) {
	source := "<<<<<<< Current deployment\nDeployed heading\n==========\n||||||| Original\nOriginal heading\n==========\n=======\nProposed heading\n==========\n>>>>>>> Your proposed changes\n"
	want := "<<<<<<< Proposed version (current change)\nProposed heading\n==========\n=======\nDeployed heading\n==========\n>>>>>>> Deployed version (incoming change)\n"
	if got, err := requestConflictText(source); err != nil || got != want {
		t.Fatalf("heading mistaken for conflict separator: %q, %v", got, err)
	}
}

func TestRequestLegacyConflictDraftIsPreserved(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	workspace.Materialized = true
	legacy := *workspace.Merge.Files[0].Merged.Content
	path := filepath.Join(workspace.Root, "SPEC.md")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := requestMergeEdits(&workspace, nil)
	var conflict *requestMergeError
	if !errors.As(err, &conflict) || conflict.code != "merge_conflicts" || strings.Contains(err.Error(), "current is your proposal") {
		t.Fatalf("legacy conflict labels misrepresented: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != legacy {
		t.Fatalf("existing draft changed: %q, %v", data, err)
	}
}

func TestMergeMarkersRecognizedRegardlessOfLabels(t *testing.T) {
	for _, marker := range []string{
		"<<<<<<< HEAD", "<<<<<<< Proposed version (current change)", "<<<<<<< Current deployment",
		"||||||| base", ">>>>>>> branch", ">>>>>>> Deployed version (incoming change)", ">>>>>>> Your proposed changes",
		"<<<<<<<", "|||||||", ">>>>>>>", "<<<<<<<<<< custom size",
	} {
		if !containsMergeMarkers("before\r\n" + marker + "\r\nafter") {
			t.Errorf("missed unresolved marker %q", marker)
		}
	}
	markdown := "A heading\n=======\n\nA normal line with <<<<<<< inside it.\n"
	if containsMergeMarkers(markdown) {
		t.Fatal("normal Markdown detected as conflict")
	}
	if got, err := requestConflictText(markdown); err != nil || got != markdown {
		t.Fatalf("clean text changed: %q, %v", got, err)
	}
}

func TestRequestTextConflictsResolvePerBlock(t *testing.T) {
	for _, choice := range []string{"current", "incoming", "both"} {
		t.Run(choice, func(t *testing.T) {
			workspace := testRequestMergeWorkspace(t)
			block := "<<<<<<< Current deployment\nDeployed setting.\n||||||| Original\nOriginal setting.\n=======\nProposed setting.\n>>>>>>> Your proposed changes\n"
			body := "Keep audit log.\n" + block + "Keep CSV export.\n" + block + "Keep final text.\n"
			source := strings.Replace(testPlanSpec, "Serve a demo.", body, 1)
			workspace.Merge.Files[0].Merged = mergeText(source)
			installTestMerge(t, &workspace)
			path := filepath.Join(workspace.Root, "SPEC.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			shown := "<<<<<<< Proposed version (current change)\nProposed setting.\n=======\nDeployed setting.\n>>>>>>> Deployed version (incoming change)\n"
			if strings.Count(string(data), shown) != 2 || strings.Contains(string(data), "Original setting.") {
				t.Fatalf("unexpected editable blocks: %s", data)
			}
			// Editing one block must leave the other unresolved, including on reload.
			first := map[string]string{"current": "Proposed setting.\n", "incoming": "Deployed setting.\n", "both": "Proposed setting.\nDeployed setting.\n"}[choice]
			partial := strings.Replace(string(data), shown, first, 1)
			if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
				t.Fatal(err)
			}
			reloaded, err := loadRequestMergeWorkspace(workspace.Root, "SPEC.md", workspace.Merge.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if *reloaded.Merge.Files[0].Merged.Content != source {
				t.Fatal("formatting changed the pinned Cloud merge")
			}
			_, _, err = requestMergeEdits(reloaded, nil)
			var conflict *requestMergeError
			if !errors.As(err, &conflict) || conflict.code != "merge_conflicts" {
				t.Fatalf("remaining block not reported: %v", err)
			}
			resolved := strings.Replace(partial, shown, "Reviewed second setting.\n", 1)
			if err := os.WriteFile(path, []byte(resolved), 0o600); err != nil {
				t.Fatal(err)
			}
			options, files, err := requestMergeEdits(reloaded, nil)
			if err != nil {
				t.Fatal(err)
			}
			wantBody := "Keep audit log.\n" + first + "Keep CSV export.\nReviewed second setting.\nKeep final text.\n"
			want := strings.Replace(testPlanSpec, "Serve a demo.", wantBody, 1)
			if len(options.Resolutions) != 1 || options.Resolutions[0].Choice != "merged" || *options.Resolutions[0].Content != want || *files["SPEC.md"].Content != want {
				t.Fatalf("per-block edits or clean changes lost: %+v", options)
			}
		})
	}
}

func TestRequestTextConflictsRejectWholeFileChoicesAndUnresolvedLocal(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	installTestMerge(t, &workspace)
	for _, choice := range []string{"current", "deployed", "proposed", "local"} {
		if _, _, err := requestMergeEdits(&workspace, []requestConflictChoice{{"SPEC.md", choice}}); err == nil {
			t.Errorf("%s bypassed text conflict resolution", choice)
		}
	}
}

func TestRequestFrontmatterConflictCanBeEditedAndResubmitted(t *testing.T) {
	workspace := testRequestMergeWorkspace(t)
	before := "name: demo"
	if !strings.Contains(testPlanSpec, before) {
		t.Fatal("fixture must have a name field")
	}
	raw := "<<<<<<< Current deployment\nname: deployed\n||||||| Original\nname: demo\n=======\nname: proposed\n>>>>>>> Your proposed changes"
	workspace.Merge.Files[0].Merged = mergeText(strings.Replace(testPlanSpec, before, raw, 1))
	installTestMerge(t, &workspace)
	path := filepath.Join(workspace.Root, "SPEC.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shown := "<<<<<<< Proposed version (current change)\nname: proposed\n=======\nname: deployed\n>>>>>>> Deployed version (incoming change)"
	if !strings.Contains(string(data), shown) {
		t.Fatalf("frontmatter conflict not formatted: %s", data)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), shown, "name: proposed", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, files, err := requestMergeEdits(&workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := *files["SPEC.md"].Content; got != strings.Replace(testPlanSpec, before, "name: proposed", 1) {
		t.Fatalf("resolved frontmatter changed: %s", got)
	}
}

func TestRequestMergeRejectsMarkersInOtherFiles(t *testing.T) {
	for _, existing := range []bool{true, false} {
		workspace := testRequestMergeWorkspace(t)
		workspace.Merge.Conflicts = nil
		workspace.Merge.Files[0].Merged = mergeText(testPlanSpec)
		workspace.SkillPaths = map[string]string{"check": "skills/check"}
		workspace.Merge.Files = append(workspace.Merge.Files,
			cloud.RequestMergeFile{Path: ".telos/skills/check.json", Kind: "skill_metadata", Merged: mergeText("{\"required\":false}\n")},
			cloud.RequestMergeFile{Path: "skills/check/SKILL.md", Merged: mergeText("Check status.\n")})
		name := "skills/check/notes.md"
		if existing {
			workspace.Merge.Files = append(workspace.Merge.Files, cloud.RequestMergeFile{Path: name, Merged: mergeText("Notes.\n")})
		}
		installTestMerge(t, &workspace)
		if err := os.WriteFile(filepath.Join(workspace.Root, name), []byte("<<<<<<< HEAD\nunresolved\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := requestMergeEdits(&workspace, nil); err == nil || !strings.Contains(err.Error(), name+" still contains conflict markers") {
			t.Fatalf("existing=%t accepted conflict markers: %v", existing, err)
		}
	}
}
