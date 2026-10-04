package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestCloudAccessCLIProcess(t *testing.T) {
	if os.Getenv("TELOS_TEST_ACCESS_COMMAND") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index == -1 {
		os.Exit(2)
	}
	cmdApply(os.Args[index+1:])
	os.Exit(0)
}

func TestCloudApplyAccessCapabilities(t *testing.T) {
	const grant = "integrations: [{id: sec_stripe, name: Stripe}]\nallowlist: [{host: api.stripe.com}]\n"
	const revoke = "integrations: []\nallowlist: []\n"
	const egress = "egress: [{host: api.example.com, credentials: sec-example}]\n"
	const publicEgress = "egress: [{host: public.example.com}]\n"
	const revokeEgress = "egress: []\n"
	for _, source := range []string{"local", "registry"} {
		for _, operation := range []string{"create", "update"} {
			for _, tc := range []struct {
				name, access, response string
				status                 int
				allowed                bool
			}{
				{"supported grant", grant, `{"deployment_spec_access":true}`, 200, true},
				{"supported revoke", revoke, `{"deployment_spec_access":true}`, 200, true},
				{"missing capability grant", grant, `{}`, 200, false},
				{"missing capability revoke", revoke, `{}`, 200, false},
				{"disabled grant", grant, `{"deployment_spec_access":false}`, 200, false},
				{"disabled revoke", revoke, `{"deployment_spec_access":false}`, 200, false},
				{"missing endpoint", grant, `{"detail":"Not Found"}`, 404, false},
				{"unavailable endpoint", revoke, `{"detail":"Unavailable"}`, 503, false},
				{"malformed response", grant, `{`, 200, false},
				{"invalid capability type", grant, `{"deployment_spec_access":"true"}`, 200, false},
				{"null capability", revoke, `{"deployment_spec_access":null}`, 200, false},
				{"supported egress", egress, `{"deployment_egress_credentials":true}`, 200, true},
				{"supported public egress", publicEgress, `{"deployment_egress_credentials":true}`, 200, true},
				{"supported revoke egress", revokeEgress, `{"deployment_egress_credentials":true}`, 200, true},
				{"old support is insufficient", egress, `{"deployment_spec_access":true}`, 200, false},
				{"old support cannot revoke egress", revokeEgress, `{"deployment_spec_access":true}`, 200, false},
				{"old support cannot grant public egress", publicEgress, `{"deployment_spec_access":true}`, 200, false},
				{"disabled egress", egress, `{"deployment_spec_access":true,"deployment_egress_credentials":false}`, 200, false},
				{"invalid egress capability", egress, `{"deployment_egress_credentials":"true"}`, 200, false},
				{"null egress capability", revokeEgress, `{"deployment_egress_credentials":null}`, 200, false},
				{"unavailable egress capability", egress, `{"detail":"Unavailable"}`, 503, false},
				{"legacy goal", "", `{"detail":"Not Found"}`, 404, true},
			} {
				t.Run(source+"/"+operation+"/"+tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "SPEC.md")
					markdown := "---\nname: demo\nversion: 1.2.3\nplatform: cloud\n" + tc.access + "---\n# Goal\nServe a demo.\n"
					if err := os.WriteFile(path, []byte(markdown), 0o600); err != nil {
						t.Fatal(err)
					}
					compiled, err := spec.CompileEnvironment(path)
					if err != nil {
						t.Fatal(err)
					}
					pkg, err := spec.BuildApplyPackage(compiled)
					if err != nil {
						t.Fatal(err)
					}
					record := cloud.PackageVersionRecord{
						Scope: "telos", Name: "demo", Version: "1.2.3",
						Ref: "@telos/demo:1.2.3", Digest: pkg.Digest,
					}
					var publications, deployments, checks atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.Method + " " + r.URL.Path {
						case "GET /api/account/bootstrap":
							_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal"}]}`))
						case "GET /api/capabilities":
							checks.Add(1)
							if r.Header.Get("Authorization") != "Bearer control-token" {
								t.Error("capability check did not use the selected Cloud client")
							}
							w.WriteHeader(tc.status)
							_, _ = w.Write([]byte(tc.response))
						case "GET /api/packages/telos/demo/versions/1.2.3":
							_ = json.NewEncoder(w).Encode(record)
						case "GET /api/packages/telos/demo/versions/1.2.3/bundle":
							_, _ = w.Write(pkg.Bytes)
						case "POST /api/packages":
							publications.Add(1)
							_ = json.NewEncoder(w).Encode(record)
						case "POST /api/deployments", "PUT /api/deployments/sess_existing":
							deployments.Add(1)
							_ = json.NewEncoder(w).Encode(cloud.SessionRecord{
								ID: "sess_existing", Name: "demo", State: "deploying",
								PackageRef: record.Ref, PackageDigest: record.Digest,
							})
						default:
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
							http.NotFound(w, r)
						}
					}))
					defer server.Close()
					configureCloudTest(t, server.URL)
					for _, key := range []string{"TELOS_CONTEXT", "TELOS_MODEL", "TELOS_THINKING", "TELOS_MAX_COST_USD", "TELOS_SESSION_ID", "TELOS_RUNTIME", "TELOS_API_TOKEN"} {
						t.Setenv(key, "")
					}
					input := path
					if source == "registry" {
						input = record.Ref
					}
					args := []string{"-test.run=^TestCloudAccessCLIProcess$", "--", input, "--json"}
					if operation == "update" {
						args = append(args, "--session", "sess_existing", "--force")
					}
					command := exec.Command(os.Args[0], args...)
					command.Env = append(os.Environ(), "TELOS_TEST_ACCESS_COMMAND=1")
					out, err := command.CombinedOutput()
					if tc.allowed {
						if err != nil || !strings.Contains(string(out), `"operation"`) {
							t.Fatalf("apply failed: %v\n%s", err, out)
						}
					} else if err == nil || !strings.Contains(string(out), "goal not applied") {
						t.Fatalf("apply did not fail closed: %v\n%s", err, out)
					}
					var wantDeployments, wantPublications, wantChecks int32
					if tc.allowed {
						wantDeployments = 1
						if source == "local" {
							wantPublications = 1
						}
					}
					if tc.access != "" {
						wantChecks = 1
					}
					if deployments.Load() != wantDeployments || publications.Load() != wantPublications || checks.Load() != wantChecks {
						t.Fatalf("deployments=%d publications=%d checks=%d; want %d/%d/%d", deployments.Load(), publications.Load(), checks.Load(), wantDeployments, wantPublications, wantChecks)
					}
				})
			}
		}
	}
}
