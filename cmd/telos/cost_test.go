package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func displayMicroUSD(value int64) *int64 { return &value }

func costDisplaySession() cloud.SessionRecord {
	service := "https://example.com"
	return cloud.SessionRecord{
		ID: "sess_cost", Name: "byok-demo", Status: "ready", PackageDigest: "sha256:abc", ServiceURL: &service,
		Inference: &cloud.InferenceSummary{Source: "byok", Provider: "anthropic"},
		Cost: &cloud.SessionCost{
			Telos:             cloud.TelosCost{SpendMicroUSD: displayMicroUSD(1250000), Categories: &cloud.CostCategories{ComputeMicroUSD: 1000000, StorageMicroUSD: 250000}},
			ExternalInference: &cloud.ExternalInferenceCost{Provider: "anthropic", EstimateKind: "api_cost", EstimatedCostMicroUSD: displayMicroUSD(3400000)},
		},
	}
}

func TestCloudCostDescriptionAlignmentAndHierarchy(t *testing.T) {
	var out bytes.Buffer
	printCloudSessionDescriptionForContext(&out, costDisplaySession(), "@team")
	want := "Name                  byok-demo\n" +
		"Status                ready\n" +
		"Session               sess_cost\n" +
		"Revision              sha256:abc\n" +
		"Inference             API key\n" +
		"Context               @team\n" +
		"Service               https://example.com\n" +
		"Cost\n" +
		"  Telos spend          $1.25\n" +
		"    Compute            $1.00\n" +
		"    Storage            $0.25\n" +
		"  Anthropic estimate  ~$3.40\n"
	if out.String() != want {
		t.Fatalf("description differs from agreed layout:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestCloudCostDescriptionManagedSubscriptionAndAdjustments(t *testing.T) {
	session := costDisplaySession()
	session.Inference = &cloud.InferenceSummary{Source: "managed"}
	session.Cost.ExternalInference = nil
	session.Cost.Telos = cloud.TelosCost{
		SpendMicroUSD:    displayMicroUSD(1500000),
		Categories:       &cloud.CostCategories{InferenceMicroUSD: 1000000, ComputeMicroUSD: 1000000},
		ReversedMicroUSD: 500000,
	}
	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	text := out.String()
	for _, want := range []string{"  Spend", "    Inference", "    Compute", "    Reversed", "−$0.50"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	for _, absent := range []string{"Telos spend", "estimate", "Storage", "Total", "\n\nCost"} {
		if strings.Contains(text, absent) {
			t.Fatalf("unexpected %q:\n%s", absent, text)
		}
	}

	session = costDisplaySession()
	session.Cost.ExternalInference = &cloud.ExternalInferenceCost{Provider: "chatgpt-codex", EstimateKind: "api_equivalent", EstimatedCostMicroUSD: displayMicroUSD(6400000)}
	session.Cost.Telos.Categories = nil
	session.Status = "needs_attention"
	session.StatusReason = "Provider requires attention"
	out.Reset()
	printCloudSessionDescription(&out, session)
	text = out.String()
	if !strings.Contains(text, "Codex estimate") || !strings.Contains(text, "~$6.40 (API-equivalent)") || !strings.Contains(text, "Detailed breakdown unavailable") {
		t.Fatalf("subscription or unavailable breakdown meaning lost:\n%s", text)
	}
	if strings.Index(text, "Reason") > strings.Index(text, "\nCost\n") || strings.Contains(text, "\n\nCost") {
		t.Fatalf("Cost must follow all metadata without a blank line:\n%s", text)
	}
}

func TestCloudCostProviderColumnFollowsDisplayedInference(t *testing.T) {
	managed := cloud.SessionRecord{ID: "managed", Inference: &cloud.InferenceSummary{Source: "managed"}, Cost: &cloud.SessionCost{Telos: cloud.TelosCost{SpendMicroUSD: displayMicroUSD(0)}}}
	external := costDisplaySession()
	external.Cost.ExternalInference.EstimatedCostMicroUSD = nil
	for _, test := range []struct {
		name     string
		sessions []cloud.SessionRecord
		provider bool
	}{
		{"managed only", []cloud.SessionRecord{managed}, false},
		{"mixed with unavailable estimate", []cloud.SessionRecord{managed, external}, true},
		{"external excluded by limit", limitCloudSessions([]cloud.SessionRecord{managed, external}, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			printCloudSessionList(&out, test.sessions, true)
			text := out.String()
			if strings.Contains(text, "PROVIDER ESTIMATE") != test.provider || !strings.Contains(text, "TELOS SPEND") {
				t.Fatalf("incorrect columns:\n%s", text)
			}
			if test.provider && (!strings.Contains(text, "Anthropic Unavailable") || !strings.Contains(text, "—")) {
				t.Fatalf("missing vs inapplicable estimates:\n%s", text)
			}
			if strings.Contains(text, "Total") || strings.Contains(text, "aggregate") {
				t.Fatalf("unexpected aggregate:\n%s", text)
			}
		})
	}
}

func TestCloudDescriptionAlignsLongLabelsAndLargeAmounts(t *testing.T) {
	session := costDisplaySession()
	session.Cost.ExternalInference.Provider = "A much longer provider name"
	session.Cost.ExternalInference.EstimatedCostMicroUSD = displayMicroUSD(1234567890000)
	session.Cost.Telos.SpendMicroUSD = displayMicroUSD(0)
	session.Cost.Telos.Categories = nil
	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	var decimalColumn = -1
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.Contains(line, "$") {
			continue
		}
		column := strings.Index(line, ".")
		if decimalColumn >= 0 && column != decimalColumn {
			t.Fatalf("amounts not right-aligned:\n%s", out.String())
		}
		decimalColumn = column
	}
}

func TestCommandsEnrichOnlyRequestedGoalsAndShareCostJSON(t *testing.T) {
	var billingCalls, estimateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/account/bootstrap":
			fmt.Fprint(w, `{"personal_org_id":"org_test","organizations":[{"id":"org_test","handle":"test","kind":"personal","role":"owner"}]}`)
		case "/api/deployments":
			fmt.Fprint(w, `{"deployments":[{"id":"sess_cost","name":"cost goal","inference":{"source":"byok","provider":"anthropic"}},{"id":"hidden","inference":{"source":"subscription","provider":"chatgpt-codex"}}]}`)
		case "/api/deployments/sess_cost":
			fmt.Fprint(w, `{"id":"sess_cost","name":"cost goal","inference":{"source":"byok","provider":"anthropic"}}`)
		case "/api/billing/deployments":
			billingCalls.Add(1)
			fmt.Fprint(w, `{"deployments":[{"deployment_id":"sess_cost","net_spend_micro_usd":1250000,"non_inference_net_spend_micro_usd":1250000,"compute_micro_usd":1000000,"storage_micro_usd":250000}]}`)
		case "/api/deployments/sess_cost/inference-usage":
			estimateCalls.Add(1)
			fmt.Fprint(w, `{"estimated_cost_micro_usd":3400000}`)
		default:
			t.Errorf("unexpected or unbounded enrichment request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "")
	compact := captureStdout(t, func() { cmdList([]string{"--limit", "1"}) })
	if billingCalls.Load() != 0 || estimateCalls.Load() != 0 || strings.Contains(compact, "SPEND") {
		t.Fatalf("compact list fetched costs: %s", compact)
	}
	wide := captureStdout(t, func() { cmdList([]string{"--wide", "--limit", "1"}) })
	if !strings.Contains(wide, "Anthropic ~$3.40") || strings.Contains(wide, "hidden") {
		t.Fatalf("wide list: %s", wide)
	}
	listJSON := captureStdout(t, func() { cmdList([]string{"--json", "--limit", "1"}) })
	detailJSON := captureStdout(t, func() { cmdDescribe([]string{"sess_cost", "--json"}) })
	var listed struct {
		Sessions []struct{ Cost cloud.SessionCost }
	}
	var described struct{ Cost cloud.SessionCost }
	if err := json.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(detailJSON), &described); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || !reflect.DeepEqual(listed.Sessions[0].Cost, described.Cost) {
		t.Fatalf("list/describe cost contracts differ:\n%s\n%s", listJSON, detailJSON)
	}
	if *described.Cost.Telos.SpendMicroUSD != 1250000 || *described.Cost.ExternalInference.EstimatedCostMicroUSD != 3400000 {
		t.Fatalf("wrong amounts: %+v", described.Cost)
	}
	for _, output := range []string{listJSON, detailJSON} {
		for _, forbidden := range []string{`"period"`, `"currency"`, `"cost_totals"`, `"reversed_micro_usd"`} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("unexpected field %s: %s", forbidden, output)
			}
		}
	}
	if billingCalls.Load() != 3 || estimateCalls.Load() != 3 {
		t.Fatalf("cost requests: billing %d inference %d", billingCalls.Load(), estimateCalls.Load())
	}
}

func TestSubscriptionEstimateMeaningSurvivesUnavailableData(t *testing.T) {
	session := costDisplaySession()
	session.Inference = &cloud.InferenceSummary{Source: "subscription", Provider: "chatgpt-codex"}
	session.Cost.ExternalInference = &cloud.ExternalInferenceCost{Provider: "chatgpt-codex", EstimateKind: "api_equivalent"}
	for _, amount := range []*int64{nil, displayMicroUSD(6400000)} {
		session.Cost.ExternalInference.EstimatedCostMicroUSD = amount
		var detail, list bytes.Buffer
		printCloudSessionDescription(&detail, session)
		printCloudSessionList(&list, []cloud.SessionRecord{session}, true)
		for _, output := range []string{detail.String(), list.String()} {
			if !strings.Contains(output, "(API-equivalent)") || (amount == nil && !strings.Contains(output, "Unavailable")) {
				t.Fatalf("subscription estimate lost meaning: %s", output)
			}
		}
	}
}
