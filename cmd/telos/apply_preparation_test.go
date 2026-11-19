package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestPackageSkillsValidateAllSkillsBeforePublishing(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected publication", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("TELOS_SKILLS_DIR", t.TempDir())
	compiled := &spec.CompiledEnvironment{}
	for _, name := range []string{"a-good", "z-invalid"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		version := "1.0.0"
		if name == "z-invalid" {
			version = "v1"
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\nversion: "+version+"\n---\nInstructions.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		compiled.Skills = append(compiled.Skills, &spec.Skill{Name: name, Path: dir})
	}
	_, err := pushPackageSkills(cloud.NewClient(server.URL, "token"), compiled, "", "")
	if err == nil || !strings.Contains(err.Error(), "must not start with v") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("sent %d requests before validating the final skill", requests.Load())
	}
}
