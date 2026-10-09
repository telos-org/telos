package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

func microUSD(value int64) *int64 { return &value }

// costServer serves a workspace with one Goal on each kind of inference and a
// Goal shared from another workspace, which comes without billing.
func costServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		billing := func(raw string) string {
			if r.URL.Query().Get("include_billing") != "true" {
				return ""
			}
			return `,"billing":` + raw
		}
		switch r.URL.Path {
		case "/api/deployments":
			_, _ = w.Write([]byte(`{"deployments":[
				{"id":"goal_telos","name":"star-tap","status":"ready","access_source":"owned","inference":{"source":"managed","tier":"default"}` +
				billing(`{"inference_micro_usd":6500000,"compute_micro_usd":1000000,"storage_micro_usd":250000}`) + `},
				{"id":"goal_key","name":"byok-demo","status":"ready","access_source":"owned","inference":{"source":"byok","provider":"anthropic"}` +
				billing(`{"inference_micro_usd":0,"compute_micro_usd":1000000,"storage_micro_usd":250000}`) + `},
				{"id":"goal_sub","name":"sub-demo","status":"ready","access_source":"owned","inference":{"source":"subscription","provider":"chatgpt-codex"}` +
				billing(`{"inference_micro_usd":0,"compute_micro_usd":0,"storage_micro_usd":0}`) + `},
				{"id":"goal_shared","name":"shared","status":"ready","access_source":"link","inference":{"source":"byok","provider":"openai"}}
			]}`))
		case "/api/deployments/goal_key/inference-usage":
			_, _ = w.Write([]byte(`{"estimated_cost_micro_usd":3400000}`))
		case "/api/deployments/goal_sub/inference-usage":
			// Cloud reports an unreachable runtime as no amount.
			_, _ = w.Write([]byte(`{"estimated_cost_micro_usd":null}`))
		default:
			// Includes runtime lookups for Goals that shouldn't need one.
			t.Errorf("unexpected request: %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
}

func TestGoalCostsSplitCloudAndInferenceByWhoBillsThem(t *testing.T) {
	server := costServer(t)
	defer server.Close()
	control := cloud.NewClient(server.URL, "token")
	goals, err := control.ListSessionsWithBilling()
	if err != nil {
		t.Fatal(err)
	}

	costs := goalCosts(control, goals)

	want := [][2]string{
		{"$1.25 (compute $1.00, storage $0.25)", "$6.50 (Telos)"},
		{"$1.25 (compute $1.00, storage $0.25)", "$3.40 (Anthropic API key)"},
		// No charges yet, and the runtime reported no amount.
		{"$0.00", "unavailable (ChatGPT subscription)"},
	}
	for index, row := range want {
		got := [2]string{cloudCostText(costs[index].Cloud), inferenceCostText(costs[index].Inference)}
		if got != row {
			t.Errorf("%s cost = %q, want %q", goals[index].Name, got, row)
		}
	}
	if costs[3] != nil {
		t.Errorf("a Goal shared from another workspace has a cost: %#v", costs[3])
	}
}

func TestGoalCostsRoundToCentsThatAddUp(t *testing.T) {
	for _, tt := range []struct {
		compute, storage int64
		want             string
	}{
		{1_004_999, 1_004_999, "$2.00 (compute $1.00, storage $1.00)"},
		{1_006_000, 1_006_000, "$2.02 (compute $1.01, storage $1.01)"},
		{2_675_000, 0, "$2.68 (compute $2.68)"},
		{15_000, 0, "$0.02 (compute $0.02)"},
		{3_000, 1_000, "<$0.01"},
		{0, 0, "$0.00"},
	} {
		if got := cloudCostText(newCloudCost(tt.compute, tt.storage)); got != tt.want {
			t.Errorf("compute %d, storage %d µUSD = %q, want %q", tt.compute, tt.storage, got, tt.want)
		}
	}
	if got := inferenceCostText(newInferenceCost("byok", "anthropic", microUSD(4_000))); got != "<$0.01 (Anthropic API key)" {
		t.Errorf("a sub-cent inference cost = %q", got)
	}
}

func TestGoalCostsReadTelosBillingWithoutInferenceSettings(t *testing.T) {
	costs := goalCosts(nil, []cloud.SessionRecord{
		// Unreadable inference settings: the charges are still Telos billing.
		{ID: "goal_unknown", Billing: &cloud.GoalBilling{InferenceMicroUSD: 970_000}},
		// Billing without category amounts.
		{ID: "goal_telos", Inference: &cloud.InferenceSummary{Source: "managed"}, Billing: &cloud.GoalBilling{InferenceMicroUSD: 10_000}},
	})

	if got := inferenceCostText(costs[0].Inference); got != "$0.97 (Telos)" {
		t.Errorf("Inference = %q", got)
	}
	if got := cloudCostText(costs[1].Cloud); got != "unavailable" {
		t.Errorf("Cloud = %q", got)
	}
}

func TestGoalCostsMarkWhatCannotBeRead(t *testing.T) {
	costs := goalCosts(nil, []cloud.SessionRecord{
		// Your Goal, but Cloud returned no billing.
		{ID: "goal_owned", AccessSource: "owned", Inference: &cloud.InferenceSummary{Source: "managed"}},
		{ID: "goal_shared", AccessSource: "link"},
		// describe's Goal detail doesn't say who owns it.
		{ID: "goal_detail"},
	})

	if cloudAmount, inference := costCells(costs[0]); cloudAmount != "?" || inference != "?" {
		t.Errorf("owned Goal without billing = %q, %q", cloudAmount, inference)
	}
	if cloudAmount, inference := costCells(costs[1]); cloudAmount != "-" || inference != "-" {
		t.Errorf("shared Goal = %q, %q", cloudAmount, inference)
	}
	if costs[2] != nil {
		t.Errorf("a Goal detail without billing has a cost: %#v", costs[2])
	}
}

func TestGoalCostsStopWaitingForSlowRuntimes(t *testing.T) {
	previous := costLookupTimeout
	costLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { costLookupTimeout = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	goals := make([]cloud.SessionRecord, 10)
	for index := range goals {
		goals[index] = cloud.SessionRecord{
			ID:        "goal",
			Inference: &cloud.InferenceSummary{Source: "byok", Provider: "anthropic"},
			Billing:   &cloud.GoalBilling{},
		}
	}

	started := time.Now()
	costs := goalCosts(cloud.NewClient(server.URL, "token"), goals)

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cost lookups took %s", elapsed)
	}
	if got := inferenceCostText(costs[9].Inference); got != "unavailable (Anthropic API key)" {
		t.Errorf("Inference = %q", got)
	}
}

func TestListShowsCostsOnlyWhenWide(t *testing.T) {
	server := costServer(t)
	defer server.Close()
	configureCloudTest(t, server.URL)

	plain := captureStdout(t, func() { cmdList([]string{"--json"}) })
	if strings.Contains(plain, `"cost"`) || strings.Contains(plain, `"billing"`) {
		t.Fatalf("plain list read costs:\n%s", plain)
	}

	wide := captureStdout(t, func() { cmdList([]string{"--wide"}) })
	rows := map[string][]string{}
	for _, line := range strings.Split(wide, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			rows[fields[0]] = fields
		}
	}
	for name, want := range map[string][]string{
		"NAME":      {"CLOUD", "INFERENCE", "SESSION"},
		"star-tap":  {"$1.25", "$6.50", "goal_telos"},
		"byok-demo": {"$1.25", "$3.40", "goal_key"},
		"sub-demo":  {"$0.00", "?", "goal_sub"},
		"shared":    {"-", "-", "goal_shared"},
	} {
		fields := rows[name]
		if len(fields) < 3 || strings.Join(fields[len(fields)-3:], " ") != strings.Join(want, " ") {
			t.Errorf("%s row = %q, want it to end with %q\n%s", name, fields, want, wide)
		}
	}

	var listed struct {
		Sessions []struct {
			Cost *goalCost `json:"cost"`
		} `json:"sessions"`
	}
	wideJSON := captureStdout(t, func() { cmdList([]string{"--wide", "--json"}) })
	if strings.Contains(wideJSON, `"billing"`) {
		t.Fatalf("list JSON exposes raw billing next to cost:\n%s", wideJSON)
	}
	if err := json.Unmarshal([]byte(wideJSON), &listed); err != nil {
		t.Fatal(err)
	}
	key := listed.Sessions[1].Cost
	if key == nil || key.Inference.USD == nil || *key.Inference.USD != 3.4 || key.Inference.Source != "byok" ||
		key.Inference.Provider != "anthropic" || *key.Cloud.ComputeUSD != 1 || *key.Cloud.StorageUSD != 0.25 {
		t.Fatalf("API key Goal cost JSON = %#v", key)
	}
}

func TestDescribeReadsTheGoalsCostInItsContext(t *testing.T) {
	var orgs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/account/bootstrap" {
			orgs = append(orgs, r.URL.Path+" "+r.Header.Get("X-Telos-Org-Id"))
		}
		switch r.URL.Path {
		case "/api/account/bootstrap":
			_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","handle":"person","role":"owner"},{"id":"org_telos","handle":"telos","role":"owner"}]}`))
		case "/api/deployments/goal_key":
			_, _ = w.Write([]byte(`{"id":"goal_key","name":"byok-demo","status":"ready","inference":{"source":"byok","provider":"anthropic"},
				"billing":{"inference_micro_usd":0,"compute_micro_usd":1000000,"storage_micro_usd":250000}}`))
		case "/api/deployments/goal_key/inference-usage":
			_, _ = w.Write([]byte(`{"estimated_cost_micro_usd":3400000}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)

	out := captureStdout(t, func() { cmdDescribe([]string{"--context", "@telos", "goal_key"}) })
	for _, want := range []string{
		"\nCloud     $1.25 (compute $1.00, storage $0.25)\n",
		"\nInference $3.40 (Anthropic API key)\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe is missing %q:\n%s", strings.TrimSpace(want), out)
		}
	}
	for _, request := range orgs {
		if !strings.HasSuffix(request, " org_telos") {
			t.Fatalf("request outside the Goal's context: %s", request)
		}
	}

	encoded := captureStdout(t, func() { cmdDescribe([]string{"--context", "@telos", "--json", "goal_key"}) })
	var described struct {
		Cost    *goalCost       `json:"cost"`
		Billing json.RawMessage `json:"billing"`
	}
	if err := json.Unmarshal([]byte(encoded), &described); err != nil {
		t.Fatal(err)
	}
	if described.Billing != nil || described.Cost == nil || *described.Cost.Inference.USD != 3.4 {
		t.Fatalf("describe --json = %s", encoded)
	}
}

func TestDescribeEndsWithCostBeforeTheReason(t *testing.T) {
	var out strings.Builder
	reason := "provider rejected the API key"
	printCloudSessionDescriptionForContext(&out, cloud.SessionRecord{
		ID: "goal_key", Name: "byok-demo", Status: "needs_attention", FailureReason: &reason,
	}, "@telos", &goalCost{
		Cloud:     newCloudCost(1_000_000, 250_000),
		Inference: newInferenceCost("byok", "anthropic", microUSD(3_400_000)),
	})

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	tail := strings.Join(lines[len(lines)-3:], "\n")
	want := "Cloud     $1.25 (compute $1.00, storage $0.25)\n" +
		"Inference $3.40 (Anthropic API key)\n" +
		"Reason    provider rejected the API key"
	if tail != want {
		t.Fatalf("describe ends with:\n%s\nwant:\n%s", tail, want)
	}
}
