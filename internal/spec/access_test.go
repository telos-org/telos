package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccessDeclarationValidation(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"undeclared", "", true},
		{"egress empty", "egress: []\n", true},
		{"egress public", "egress: [{host: api.example.com}]\n", true},
		{"egress credential", "egress: [{host: api.example.com, credentials: sec-example}]\n", true},
		{"egress old ID", "egress: [{host: api.example.com, credentials: sec_old_id}]\n", true},
		{"egress shared credential", "egress: [{host: api.example.com, credentials: sec-example}, {host: uploads.example.com, credentials: sec-example}]\n", true},
		{"egress scoped", "egress: [{host: api.example.com, credentials: sec-example, methods: [GET], paths: ['/reports/*']}]\n", true},
		{"egress scoped same host", "egress: [{host: api.example.com, credentials: sec-one, paths: ['/a/*']}, {host: api.example.com, credentials: sec-two, paths: ['/b/*']}]\n", true},
		{"egress public wildcard", "egress: [{host: '*.example.com'}]\n", true},
		{"egress credential wildcard", "egress: [{host: '*.example.com', credentials: sec-example}]\n", false},
		{"egress credential path glob", "egress: [{host: api.example.com, credentials: sec-example, paths: ['/reports*']}]\n", false},
		{"egress credential prefix path glob", "egress: [{host: api.example.com, credentials: sec-example, paths: ['/reports/*/files/*']}]\n", false},
		{"egress mixed integrations", "egress: []\nintegrations: []\n", false},
		{"egress mixed allowlist", "egress: []\nallowlist: []\n", false},
		{"egress null", "egress: null\n", false},
		{"egress scalar", "egress: api.example.com\n", false},
		{"egress bad row", "egress: [api.example.com]\n", false},
		{"egress name", "egress: [{host: api.example.com, credentials: sec-example, name: Example}]\n", false},
		{"egress credential object", "egress: [{host: api.example.com, credentials: {id: sec-example, name: Example}}]\n", false},
		{"egress credential list", "egress: [{host: api.example.com, credentials: [sec-example]}]\n", false},
		{"egress credential null", "egress: [{host: api.example.com, credentials: null}]\n", false},
		{"egress credential empty", "egress: [{host: api.example.com, credentials: ''}]\n", false},
		{"egress credential new underscore", "egress: [{host: api.example.com, credentials: sec-example_key}]\n", false},
		{"egress secret value", "egress: [{host: api.example.com, credentials: sk_live_key}]\n", false},
		{"egress methods star", "egress: [{host: api.example.com, methods: ['*']}]\n", false},
		{"egress method too long", "egress: [{host: api.example.com, methods: [ABCDEFGHIJKLMNOPQ]}]\n", false},
		{"egress paths star", "egress: [{host: api.example.com, paths: ['*']}]\n", false},
		{"egress empty scopes", "egress: [{host: api.example.com, credentials: sec-example, methods: [], paths: []}]\n", true},
		{"empty", "integrations: []\nallowlist: []\n", true},
		{"integration only", "integrations: []\n", false},
		{"allowlist only", "allowlist: []\n", false},
		{"null", "integrations: null\nallowlist: []\n", false},
		{"scalar", "integrations: sec_valid\nallowlist: []\n", false},
		{"credentials", "integrations: [{key: STRIPE_KEY, value: secret}]\nallowlist: []\n", false},
		{"wrong ID", "integrations: [stripe]\nallowlist: []\n", false},
		{"ID whitespace", "integrations: [' sec_valid']\nallowlist: []\n", false},
		{"duplicate IDs", "integrations: [sec_valid, sec_valid]\nallowlist: []\n", false},
		{"named", "integrations: [{id: sec_valid, name: Stripe}]\nallowlist: []\n", true},
		{"mixed", "integrations: [sec_old, {id: sec_valid, name: Stripe}]\nallowlist: []\n", true},
		{"named missing ID", "integrations: [{name: Stripe}]\nallowlist: []\n", false},
		{"named missing name", "integrations: [{id: sec_valid}]\nallowlist: []\n", false},
		{"named unknown field", "integrations: [{id: sec_valid, name: Stripe, token: secret}]\nallowlist: []\n", false},
		{"named wrong ID", "integrations: [{id: stripe, name: Stripe}]\nallowlist: []\n", false},
		{"named null name", "integrations: [{id: sec_valid, name: null}]\nallowlist: []\n", false},
		{"named empty name", "integrations: [{id: sec_valid, name: '  '}]\nallowlist: []\n", false},
		{"named duplicate ID", "integrations: [{id: sec_valid, name: Stripe}, {id: sec_valid, name: Other}]\nallowlist: []\n", false},
		{"mixed duplicate ID", "integrations: [sec_valid, {id: sec_valid, name: Stripe}]\nallowlist: []\n", false},
		{"mixed reverse duplicate ID", "integrations: [{id: sec_valid, name: Stripe}, sec_valid]\nallowlist: []\n", false},
		{"reserved name", "integrations: [{id: sec_valid, name: 'tElOs MaNaGeD / Test'}]\nallowlist: []\n", false},
		{"reserved Unicode fold", "integrations: [{id: sec_valid, name: 'Teloſ managed / Test'}]\nallowlist: []\n", false},
		{"scoped", "integrations: [sec_valid]\nallowlist: [{host: api.stripe.com, methods: [GET, POST], paths: ['/v1/*']}]\n", true},
		{"wildcard", "integrations: []\nallowlist: [{host: '*.example.com'}]\n", true},
		{"URL host", "integrations: []\nallowlist: [{host: 'https://api.stripe.com'}]\n", false},
		{"IP host", "integrations: []\nallowlist: [{host: 127.0.0.1}]\n", false},
		{"port", "integrations: []\nallowlist: [{host: 'api.stripe.com:443'}]\n", false},
		{"empty host", "integrations: []\nallowlist: [{}]\n", false},
		{"extra rule field", "integrations: []\nallowlist: [{host: api.stripe.com, token: secret}]\n", false},
		{"method case", "integrations: []\nallowlist: [{host: api.stripe.com, methods: [get]}]\n", false},
		{"unicode method", "integrations: []\nallowlist: [{host: api.stripe.com, methods: [GÉT]}]\n", false},
		{"duplicate methods", "integrations: []\nallowlist: [{host: api.stripe.com, methods: [GET, GET]}]\n", false},
		{"null paths", "integrations: []\nallowlist: [{host: api.stripe.com, paths: null}]\n", false},
		{"encoded path", "integrations: []\nallowlist: [{host: api.stripe.com, paths: ['/a%2fb']}]\n", false},
		{"dot path", "integrations: []\nallowlist: [{host: api.stripe.com, paths: ['/a/../b']}]\n", false},
		{"double slash", "integrations: []\nallowlist: [{host: api.stripe.com, paths: ['/a//b']}]\n", false},
		{"duplicate paths", "integrations: []\nallowlist: [{host: api.stripe.com, paths: ['/', '/']}]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _, ok := ParseFrontmatter("---\n" + tc.fields + "name: test\n---\nBody")
			if !ok {
				t.Fatal("fixture did not parse")
			}
			access, err := ParseAccess(raw)
			if tc.valid != (err == nil) {
				t.Fatalf("access=%#v error=%v, want valid=%t", access, err, tc.valid)
			}
			if tc.name == "undeclared" && access != nil {
				t.Fatal("missing declaration must stay distinguishable from empty")
			}
			if tc.name == "empty" && (access == nil || access.Integrations == nil || access.Allowlist == nil) {
				t.Fatal("explicit empty declaration must contain empty arrays")
			}
			if tc.name == "egress empty" && (access == nil || access.Egress == nil || len(access.Egress) != 0) {
				t.Fatal("explicit empty egress must stay distinguishable from legacy and undeclared access")
			}
		})
	}
}

func TestIntegrationNamesUseWorkspaceNameLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{" \u2003Stripe production\u2003 ", true},
		{strings.Repeat("☕", 120), true},
		{strings.Repeat("☕", 121), false},
		{"Stripe\nproduction", false},
		{"Stripe\tproduction", false},
		{"Stripe\x00production", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, err := ParseAccess(map[string]interface{}{
				"integrations": []interface{}{map[string]interface{}{"id": "sec_stripe", "name": tc.name}},
				"allowlist":    []interface{}{},
			})
			if tc.valid != (err == nil) {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
			if tc.valid && (access.Integrations[0] != "sec_stripe" || access.IntegrationNames["sec_stripe"] != strings.TrimSpace(tc.name)) {
				t.Fatalf("name changed identity or lost its label: %#v", access)
			}
		})
	}
}

func TestAccessLimitsAndNormalization(t *testing.T) {
	ids := make([]interface{}, 101)
	if _, err := ParseAccess(map[string]interface{}{"integrations": ids, "allowlist": []interface{}{}}); err == nil {
		t.Fatal("oversized integration list accepted")
	}
	if _, err := ParseAccess(map[string]interface{}{"integrations": []interface{}{}, "allowlist": make([]interface{}, 10001)}); err == nil {
		t.Fatal("oversized allowlist accepted")
	}
	if _, err := ParseAccess(map[string]interface{}{"egress": make([]interface{}, 10001)}); err == nil {
		t.Fatal("oversized egress accepted")
	}
	rules := make([]interface{}, 101)
	for i := range rules {
		rules[i] = map[string]interface{}{"host": "api.example.com", "credentials": fmt.Sprintf("sec-%d", i)}
	}
	if _, err := ParseAccess(map[string]interface{}{"egress": rules}); err == nil {
		t.Fatal("more than 100 unique credentials accepted")
	}
	if _, err := ParseAccess(map[string]interface{}{"egress": rules[:100]}); err != nil {
		t.Fatalf("100 unique credential references must fit the schema: %v", err)
	}
	raw, _, _ := ParseFrontmatter("---\nintegrations: []\nallowlist: [{host: ' API.Example.com '}, {host: api.example.com, methods: [], paths: []}]\n---\nBody")
	access, err := ParseAccess(raw)
	if err != nil || len(access.Allowlist) != 1 || access.Allowlist[0].Host != "api.example.com" {
		t.Fatalf("normalization did not match Cloud: %#v %v", access, err)
	}
}

func TestAccessYAMLReferencesAndDuplicateKeys(t *testing.T) {
	for _, fields := range []string{
		"integrations: []\nintegrations: [sec_hidden]\nallowlist: []\n",
		"integrations: []\nallowlist: [{host: safe.example.com, host: other.example.com}]\n",
		"egress: []\negress: [{host: other.example.com}]\n",
		"egress: [{host: api.example.com, credentials: sec-one, credentials: sec-two}]\n",
	} {
		if _, _, ok := ParseFrontmatter("---\n" + fields + "---\nBody"); ok {
			t.Fatal("ambiguous duplicate YAML keys accepted")
		}
	}
	for _, fields := range []string{
		"refs: &refs [sec_stripe]\nintegrations: *refs\nallowlist: [{host: api.stripe.com}]\n",
		"defaults: &defaults {integrations: [sec_stripe], allowlist: [{host: api.stripe.com}]}\n<<: *defaults\n",
		"defaults: &defaults {integrations: [sec_replaced], allowlist: [{host: api.stripe.com}]}\n<<: *defaults\nintegrations: [sec_stripe]\n",
	} {
		raw, _, ok := ParseFrontmatter("---\n" + fields + "---\nBody")
		if !ok {
			t.Fatal("valid YAML reference rejected")
		}
		access, err := ParseAccess(raw)
		if err != nil || len(access.Integrations) != 1 || access.Integrations[0] != "sec_stripe" || access.Allowlist[0].Host != "api.stripe.com" {
			t.Fatalf("YAML reference changed access: %#v %v", access, err)
		}
	}
}

func TestEgressSurvivesPackagingWithBindingsAndChangesHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SPEC.md")
	text := "---\nname: access-test\nversion: 1.0.0\nplatform: cloud\negress:\n  - host: api.example.com\n    credentials: sec-example\n  - host: public.example.com\n---\nBody\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ToIRJSON(compiled)["access"])
	want := `{"egress":[{"host":"api.example.com","methods":[],"paths":[],"credentials":"sec-example"},{"host":"public.example.com","methods":[],"paths":[]}]}`
	if err != nil || string(encoded) != want {
		t.Fatalf("egress lost bindings or unrestricted defaults: %s %v", encoded, err)
	}
	var decoded AccessSpec
	if err := json.Unmarshal(encoded, &decoded); err != nil || len(decoded.Egress) != 2 || decoded.Egress[0].Credentials != "sec-example" {
		t.Fatalf("plan JSON lost credential bindings on decode: %#v %v", decoded, err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tarEntries(t, pkg.Bytes)["SPEC.md"], []byte(text)) {
		t.Fatal("package changed the authoritative frontmatter")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, "sec-example", "sec-other")), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := CompileEnvironment(path)
	if err != nil || changed.ContentHash == compiled.ContentHash {
		t.Fatalf("credential binding must affect content hash: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, "platform: cloud", "platform: local")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileEnvironment(path); err == nil || !strings.Contains(err.Error(), "require platform: cloud") {
		t.Fatalf("local spec claimed Cloud access enforcement: %v", err)
	}
}

func TestAccessSurvivesCompilePackageAndChangesHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SPEC.md")
	text := "---\nname: access-test\nversion: 1.0.0\nplatform: cloud\nintegrations: [{id: sec_stripe, name: Stripe production}]\nallowlist: [{host: api.stripe.com, methods: [POST], paths: ['/v1/*']}]\n---\nBody\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ToIRJSON(compiled)["access"])
	if err != nil || !strings.Contains(string(encoded), `"integrations":["sec_stripe"]`) || !strings.Contains(string(encoded), `"integration_names":{"sec_stripe":"Stripe production"}`) || !strings.Contains(string(encoded), `"paths":["/v1/*"]`) {
		t.Fatalf("access missing from IR: %s %v", encoded, err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tarEntries(t, pkg.Bytes)["SPEC.md"], []byte(text)) {
		t.Fatal("package changed the authoritative frontmatter")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, "Stripe production", "Stripe test")), 0o600); err != nil {
		t.Fatal(err)
	}
	renamed, err := CompileEnvironment(path)
	if err != nil || renamed.ContentHash == compiled.ContentHash {
		t.Fatalf("label edit must still change the spec revision hash: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, "sec_stripe", "sec_other")), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ContentHash == compiled.ContentHash {
		t.Fatal("integration change must change the content hash")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, "platform: cloud", "platform: local")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileEnvironment(path); err == nil || !strings.Contains(err.Error(), "require platform: cloud") {
		t.Fatalf("local spec claimed Cloud access enforcement: %v", err)
	}
}
