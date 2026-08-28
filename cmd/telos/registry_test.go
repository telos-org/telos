package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestParseRegistryReferenceKeepsIdentitySeparateFromVersion(t *testing.T) {
	identity, err := parseRegistryReference("@alice/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ref != "@alice/postgres" || identity.version != "" {
		t.Fatalf("identity = %+v", identity)
	}
	exact, err := parseRegistryReference("@alice/postgres:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if exact.ref != "@alice/postgres" || exact.version != "1.2.3" {
		t.Fatalf("exact ref = %+v", exact)
	}
	for _, invalid := range []string{
		"alice/postgres",
		"@Alice/postgres",
		"@alice/postgres:latest",
		"@alice/postgres:1.2.3:extra",
	} {
		if _, err := parseRegistryReference(invalid); err == nil {
			t.Fatalf("parseRegistryReference(%q) succeeded", invalid)
		}
	}
}

func TestPullRegistrySkillDownloadsVerifiesAndExtractsExactVersion(t *testing.T) {
	digest, bundle := testRegistrySkillBundle(t, "verify-quality")
	srv := registrySkillPullServer(t, digest, bundle)
	defer srv.Close()
	reference, err := parseRegistryReference("@telos/verify-quality:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "pulled-skill")
	path, record, err := pullRegistrySkill(
		cloud.NewClient(srv.URL, ""),
		reference,
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || record.Digest != digest {
		t.Fatalf("pull = %q, %+v", path, record)
	}
	if _, err := os.Stat(filepath.Join(destination, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md: %v", err)
	}
}

func TestCmdPullRoutesSkillWithInterspersedFlags(t *testing.T) {
	tests := []struct {
		name string
		args func(string) []string
	}{
		{
			name: "resource first",
			args: func(destination string) []string {
				return []string{
					"skill",
					"@telos/verify-quality:1.2.3",
					"--output",
					destination,
				}
			},
		},
		{
			name: "flags first",
			args: func(destination string) []string {
				return []string{
					"--context",
					"personal",
					"--output",
					destination,
					"skill",
					"@telos/verify-quality:1.2.3",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			digest, bundle := testRegistrySkillBundle(t, "verify-quality")
			srv := registrySkillPullServer(t, digest, bundle)
			defer srv.Close()
			configureCloudTest(t, srv.URL)
			destination := filepath.Join(t.TempDir(), "pulled-skill")

			output := captureStdout(t, func() {
				cmdPull(tt.args(destination))
			})

			if !strings.Contains(output, "pulled @telos/verify-quality:1.2.3") {
				t.Fatalf("pull output = %q", output)
			}
			if _, err := os.Stat(filepath.Join(destination, "SKILL.md")); err != nil {
				t.Fatalf("SKILL.md: %v", err)
			}
		})
	}
}

func TestPullRegistrySkillDoesNotModifyExistingDestination(t *testing.T) {
	digest, bundle := testRegistrySkillBundle(t, "verify-quality")
	srv := registrySkillPullServer(t, digest, bundle)
	defer srv.Close()
	reference, err := parseRegistryReference("@telos/verify-quality:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "pulled-skill")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = pullRegistrySkill(cloud.NewClient(srv.URL, ""), reference, destination)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("pull error = %v", err)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("existing destination changed: data=%q err=%v", data, readErr)
	}
}

func TestPullRegistrySkillVerificationFailureDoesNotCreateDestination(t *testing.T) {
	_, bundle := testRegistrySkillBundle(t, "verify-quality")
	srv := registrySkillPullServer(t, "sha256:not-the-bundle", bundle)
	defer srv.Close()
	reference, err := parseRegistryReference("@telos/verify-quality:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "pulled-skill")

	_, _, err = pullRegistrySkill(cloud.NewClient(srv.URL, ""), reference, destination)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("pull error = %v", err)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed pull destination exists: %v", statErr)
	}
}

func registrySkillPullServer(t *testing.T, digest string, bundle []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/skills/telos/verify-quality/versions/1.2.3":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"scope": "telos", "name": "verify-quality", "version": "1.2.3",
				"ref": "@telos/verify-quality:1.2.3", "digest": digest,
				"file_count": 1, "source_ref": "@telos/verify-quality:1.2.3",
				"visibility": "public", "can_manage": false,
			})
		case "/api/skills/telos/verify-quality/versions/1.2.3/bundle":
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRequireRegistryPrivacyCapabilityFailsClosed(t *testing.T) {
	enabled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capabilities" {
			http.NotFound(w, r)
			return
		}
		if enabled {
			_, _ = w.Write([]byte(`{"registry_privacy":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"registry_privacy":false}`))
	}))
	defer srv.Close()
	client := cloud.NewClient(srv.URL, "")

	err := requireRegistryPrivacyCapability(client)
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disabled capability error = %v", err)
	}
	enabled = true
	if err := requireRegistryPrivacyCapability(client); err != nil {
		t.Fatalf("enabled capability rejected: %v", err)
	}
}

func TestPrintVisibilityPreflightShowsAllVersionsWarningAndBlockers(t *testing.T) {
	warning := "Making this public will expose every immutable version."
	blockedRef := "@alice/lint:1.0.0"
	output := captureStdout(t, func() {
		printVisibilityPreflight(&cloud.RegistryVisibilityPreflight{
			Scope:             "alice",
			Name:              "postgres",
			CurrentVisibility: "private",
			TargetVisibility:  "public",
			VersionCount:      3,
			Warning:           &warning,
			Blockers: []cloud.RegistryVisibilityBlocker{{
				Code:    "dependency_private",
				Message: "The exact skill dependency is private.",
				Ref:     &blockedRef,
			}},
		})
	})
	for _, expected := range []string{
		"@alice/postgres visibility: private -> public",
		"Affected immutable versions: 3",
		warning,
		blockedRef,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("visibility output missing %q:\n%s", expected, output)
		}
	}
}
