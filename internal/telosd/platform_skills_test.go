package telosd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/spec"
)

func TestInstallPlatformSkills(t *testing.T) {
	digest, bundle := platformSkillBundle(t, "\n# Telos Cloud\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/skills/blobs/"+digest+"/bundle" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer runtime-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write(bundle)
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TELOS_PACKAGE_BUNDLE_BASE_URL", server.URL+"/api/packages/blobs")
	t.Setenv(platformSkillsEnv, fmt.Sprintf(`[{"name":"telos-cloud","digest":%q}]`, digest))
	materializer := newApplyPackageMaterializer(t.TempDir(), "runtime-token")
	if err := installPlatformSkills(context.Background(), materializer); err != nil {
		t.Fatal(err)
	}

	installed, err := os.ReadFile(filepath.Join(home, ".agents", "skills", "telos-cloud", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "# Telos Cloud") {
		t.Fatalf("installed skill = %q", installed)
	}
}

func TestInstallPlatformSkillsReplacesExistingVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".agents", "skills")
	target := filepath.Join(root, "telos-cloud")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	digest, bundle := platformSkillBundle(t, "")
	if err := installPlatformSkill(
		root,
		platformSkill{Name: "telos-cloud", Digest: digest},
		bundle,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file error = %v", err)
	}
}

func platformSkillBundle(t *testing.T, body string) (string, []byte) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "telos-cloud")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: telos-cloud\ndescription: Managed Cloud.\n---\n" + body
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := spec.LoadSkill(source)
	if err != nil {
		t.Fatal(err)
	}
	digest, bundle, err := spec.BuildSkillBundle(loaded)
	if err != nil {
		t.Fatal(err)
	}
	return digest, bundle
}

func TestConfiguredPlatformSkillsRejectsDuplicateNames(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	t.Setenv(platformSkillsEnv, fmt.Sprintf(
		`[{"name":"telos-cloud","digest":%q},{"name":"telos-cloud","digest":%q}]`,
		digest,
		digest,
	))
	if _, err := configuredPlatformSkills(); err == nil || !strings.Contains(err.Error(), "duplicate platform skill") {
		t.Fatalf("configuredPlatformSkills error = %v", err)
	}
}
