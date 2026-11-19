package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	_, err := pushSpecPackageWithVisibility(cloud.NewClient(server.URL, "token"), &specPackage{compiled: compiled}, "", "")
	if err == nil || !strings.Contains(err.Error(), "must not start with v") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("sent %d requests before validating the final skill", requests.Load())
	}
}

func TestApplyValidatesLocalUpdateBeforePublishing(t *testing.T) {
	fixture := makeDryRunSkillFixture(t)
	specData := strings.ReplaceAll(string(mustReadFile(t, fixture.path)), "1.2.3", "1.2.4")
	if err := os.WriteFile(fixture.path, []byte(specData), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: verify-test\n---\nVerify new behavior.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handlers := registryApplyHandlers(fixture.pkg)
	handlers["GET /api/account/bootstrap"] = dryRunPublishScope
	handlers["GET /api/skills/telos/verify-test"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.SkillRecord{
			Scope: "telos", Name: "verify-test", Ref: "@telos/verify-test:1.0.0", Digest: fixture.digest,
		})
	}
	handlers["GET /api/packages/telos/demo/versions/1.2.4"] = http.NotFound
	handlers["POST /api/skills"] = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, pinned := body["version"]; pinned {
			t.Error("apply pinned a registry-assigned skill version")
		}
		_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Ref: "@telos/verify-test:1.0.1"})
	}
	handlers["POST /api/packages"] = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Data    string `json:"data_base64"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		data, err := base64.StdEncoding.DecodeString(body.Data)
		if err != nil {
			t.Error(err)
		}
		_, manifest, err := spec.ApplyPackageSpec(data)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid package", http.StatusBadRequest)
			return
		}
		if body.Name != "demo" || body.Version != "1.2.4" || manifest.Skills["verify-test"].Ref != "@telos/verify-test:1.0.1" {
			t.Errorf("package did not use the published skill: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{
			Ref: "@telos/demo:1.2.4", Digest: spec.ApplyPackageDigest(manifest),
		})
	}
	handlers["PUT /api/deployments/goal_test"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.SessionRecord{
			ID: "goal_test", Name: "demo", State: "deploying", PackageRef: "@telos/demo:1.2.4",
		})
	}
	calls := make(chan string, 32)
	for name, handler := range handlers {
		handlers[name] = func(w http.ResponseWriter, r *http.Request) {
			calls <- name
			handler(w, r)
		}
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	output := captureStdout(t, func() { cmdApply([]string{fixture.path, "--goal", "goal_test", "--json"}) })
	if !strings.Contains(output, `"operation": "updated"`) {
		t.Fatalf("apply failed: %s", output)
	}
	var ordered []string
	for len(calls) > 0 {
		ordered = append(ordered, <-calls)
	}
	firstWrite := slices.Index(ordered, "POST /api/skills")
	for _, read := range []string{
		"GET /api/skills/telos/verify-test",
		"GET /api/packages/telos/demo/versions/1.2.4",
		"GET /api/deployments/goal_test",
		"GET /api/packages/telos/demo/versions/1.2.3/bundle",
	} {
		if index := slices.Index(ordered, read); index < 0 || index >= firstWrite {
			t.Errorf("%s did not precede publication: %v", read, ordered)
		}
	}
}
