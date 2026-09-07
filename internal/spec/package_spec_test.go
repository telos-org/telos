package spec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPackageRelocatesAuthoredSkillPaths(t *testing.T) {
	for _, registry := range []bool{false, true} {
		for _, layout := range []string{"rubrics", "absolute", "different-name"} {
			name := layout + "/embedded"
			if registry {
				name = layout + "/registry"
			}
			t.Run(name, func(t *testing.T) {
				source := t.TempDir()
				skillDir := filepath.Join(source, "rubrics", "alpha")
				ref := "./rubrics/alpha*"
				switch layout {
				case "absolute":
					ref = skillDir + "*"
				case "different-name":
					skillDir = filepath.Join(source, "rubrics", "review")
					ref = "./rubrics/review*"
				}
				if err := os.MkdirAll(skillDir, 0o755); err != nil {
					t.Fatal(err)
				}
				skillData := []byte("---\nname: alpha\ndescription: Review acceptance requirements.\n---\nRead [checks](references/checks.md).\n")
				if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillData, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
					t.Fatal(err)
				}
				checks := []byte("Verify saved records survive a restart.\n")
				if err := os.WriteFile(filepath.Join(skillDir, "references", "checks.md"), checks, 0o644); err != nil {
					t.Fatal(err)
				}
				writePackageTestSkill(t, source, "beta", map[string]string{
					"SKILL.md": "---\nname: beta\ndescription: Implement the product.\n---\nBuild the service.\n",
				})
				body := "# Goal\n\nKeep every requirement and all rubric stars.\n"
				original := []byte("---\nname: portable-package\nversion: 1.0.0\nplatform: cloud\nskills: [\"" + ref + "\", beta]\n---\n\n" + body)
				path := filepath.Join(source, "SPEC.md")
				if err := os.WriteFile(path, original, 0o644); err != nil {
					t.Fatal(err)
				}
				compiled, err := CompileEnvironment(path)
				if err != nil {
					t.Fatal(err)
				}
				var refs map[string]string
				if registry {
					refs = map[string]string{"alpha": "@example/alpha:1.0.0", "beta": "@example/beta:1.0.0"}
				}
				pkg, err := BuildApplyPackageWithSkillRefs(compiled, refs)
				if err != nil {
					t.Fatal(err)
				}
				if !pkg.Manifest.Skills["alpha"].Starred {
					t.Fatal("lost required rubric lock")
				}
				if pkg.Manifest.Skills["beta"].Starred {
					t.Fatal("made an implementation skill a required rubric")
				}
				if !bytes.HasSuffix(tarEntries(t, pkg.Bytes)["SPEC.md"], []byte("---\n\n"+body)) {
					t.Fatal("changed spec body")
				}
				unchanged, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(original, unchanged) {
					t.Fatal("modified authored spec")
				}
				data := pkg.Bytes
				if registry {
					bundles := map[string][]byte{}
					for _, skill := range compiled.Skills {
						_, bundle, err := BuildSkillBundle(skill)
						if err != nil {
							t.Fatal(err)
						}
						bundles[skill.Name] = bundle
					}
					data, _, err = HydrateApplyPackage(data, func(req ApplyPackageSkillFetchRequest) ([]byte, error) {
						if req.Ref != refs[req.Name] {
							t.Fatalf("unexpected ref: %s", req.Ref)
						}
						return bundles[req.Name], nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				dest := t.TempDir()
				if _, err := ExtractApplyPackage(data, dest); err != nil {
					t.Fatal(err)
				}
				relocated, err := CompileEnvironment(filepath.Join(dest, "SPEC.md"))
				if err != nil {
					t.Fatal(err)
				}
				if len(relocated.Skills) != 2 || relocated.Skills[0].Path != filepath.Join(dest, "skills", "alpha") || relocated.Skills[1].Path != filepath.Join(dest, "skills", "beta") {
					t.Fatal("skill did not resolve within extracted package")
				}
				if len(relocated.RequiredVerifierSkills) != 1 || relocated.RequiredVerifierSkills[0].Name != "alpha" {
					t.Fatal("lost required rubric after relocation")
				}
				for relative, want := range map[string][]byte{"SKILL.md": skillData, "references/checks.md": checks} {
					got, err := os.ReadFile(filepath.Join(dest, "skills", "alpha", relative))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, want) {
						t.Fatalf("changed skill file %s", relative)
					}
				}
			})
		}
	}
}
