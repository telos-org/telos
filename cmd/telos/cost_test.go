package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

// costServer serves a workspace with one Goal on each kind of inference and a
// Goal shared from another workspace, which comes without billing.
func costServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.RequestURI())
		mu.Unlock()
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
				{"id":"goal_telos","name":"star-tap","status":"ready","inference":{"source":"managed","tier":"default"}` +
				billing(`{"inference_micro_usd":6500000,"compute_micro_usd":1000000,"storage_micro_usd":250000}`) + `},
				{"id":"goal_key","name":"byok-demo","status":"ready","inference":{"source":"byok","provider":"anthropic"}` +
				billing(`{"inference_micro_usd":0,"compute_micro_usd":1000000,"storage_micro_usd":250000}`) + `},
				{"id":"goal_sub","name":"sub-demo","status":"ready","inference":{"source":"subscription","provider":"chatgpt-codex"}` +
				billing(`{"inference_micro_usd":0,"compute_micro_usd":0,"storage_micro_usd":0}`) + `},
				{"id":"goal_shared","name":"shared","status":"ready","inference":{"source":"byok","provider":"openai"}}
			]}`))
		case "/api/deployments/goal_key/inference-usage":
			_, _ = w.Write([]byte(`{"estimated_cost_micro_usd":3400000}`))
		case "/api/deployments/goal_sub/inference-usage":
			http.Error(w, `{"detail":"runtime unavailable"}`, http.StatusConflict)
		default:
			t.Errorf("unexpected request: %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func TestGoalCostsSplitCloudAndInferenceByWhoBillsThem(t *testing.T) {
	server, requests := costServer(t)
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
		// No charges yet, and the runtime cost could not be read.
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
	for _, request := range requests() {
		if strings.Contains(request, "goal_telos/") || strings.Contains(request, "goal_shared/") {
			t.Errorf("looked up the runtime cost of a Goal that doesn't need it: %s", request)
		}
	}
}

func TestGoalCostsWithoutCategoryAmountsShowCloudUnavailable(t *testing.T) {
	inference := int64(970_000)
	costs := goalCosts(nil, []cloud.SessionRecord{{
		ID:        "goal_telos",
		Inference: &cloud.InferenceSummary{Source: "managed"},
		Billing:   &cloud.GoalBilling{InferenceMicroUSD: inference},
	}})

	if got := cloudCostText(costs[0].Cloud); got != "unavailable" {
		t.Errorf("Cloud = %q", got)
	}
	if got := inferenceCostText(costs[0].Inference); got != "$0.97 (Telos)" {
		t.Errorf("Inference = %q", got)
	}
}

func TestListShowsCostsOnlyWhenWide(t *testing.T) {
	server, requests := costServer(t)
	defer server.Close()
	configureCloudTest(t, server.URL)

	plain := captureStdout(t, func() { cmdList([]string{"--json"}) })
	if strings.Contains(plain, `"cost"`) || strings.Contains(plain, `"billing"`) ||
		strings.Contains(strings.Join(requests(), " "), "include_billing") {
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
			ID   string    `json:"id"`
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

func TestDescribeEndsWithCostBeforeTheReason(t *testing.T) {
	usd := func(value float64) *float64 { return &value }
	var out strings.Builder
	reason := "provider rejected the API key"
	printCloudSessionDescriptionForContext(&out, cloud.SessionRecord{
		ID: "goal_key", Name: "byok-demo", Status: "needs_attention", FailureReason: &reason,
	}, "@telos", &goalCost{
		Cloud:     cloudCost{USD: usd(1.25), ComputeUSD: usd(1), StorageUSD: usd(0.25)},
		Inference: inferenceCost{USD: usd(3.4), Source: "byok", Provider: "anthropic"},
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
