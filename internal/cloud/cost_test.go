package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func costMicroUSD(value int64) *int64 { return &value }

func TestSessionCostsUseLedgerAndSeparateExternalEstimates(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()
		if r.Header.Get("X-Telos-Org-Id") != "org_team" {
			t.Errorf("cost request lost context: %v", r.Header)
		}
		switch r.URL.Path {
		case "/api/billing/deployments":
			fmt.Fprint(w, `{"deployments":[
				{"deployment_id":"managed","gross_spend_micro_usd":4200000,"net_spend_micro_usd":4200000,"inference_micro_usd":3000000,"compute_micro_usd":1000000,"storage_micro_usd":200000,"accounting":{"state":"stale","as_of":"2026-10-06T17:00:00Z","sources":[]}},
				{"deployment_id":"byok","gross_spend_micro_usd":9250000,"net_spend_micro_usd":9250000,"non_inference_net_spend_micro_usd":1250000,"inference_micro_usd":8000000,"compute_micro_usd":1000000,"storage_micro_usd":250000},
				{"deployment_id":"subscription","gross_spend_micro_usd":800000,"net_spend_micro_usd":800000,"non_inference_net_spend_micro_usd":800000,"compute_micro_usd":800000}
			]}`)
		case "/api/deployments/byok/inference-usage":
			fmt.Fprint(w, `{"estimated_cost_micro_usd":3400000}`)
		case "/api/deployments/subscription/inference-usage":
			fmt.Fprint(w, `{"estimated_cost_micro_usd":6400000}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "token")
	client.OrgID = "org_team"
	sessions := []SessionRecord{
		{ID: "managed", Inference: &InferenceSummary{Source: "managed"}},
		{ID: "byok", Inference: &InferenceSummary{Source: "byok", Provider: "anthropic"}},
		{ID: "subscription", Inference: &InferenceSummary{Source: "subscription", Provider: "chatgpt-codex"}},
		{ID: "missing", Inference: &InferenceSummary{Source: "managed"}},
	}
	client.PopulateSessionCosts(sessions)
	managed := sessions[0].Cost
	if managed == nil || *managed.Telos.SpendMicroUSD != 4200000 || managed.ExternalInference != nil {
		t.Fatalf("managed cost: %+v", managed)
	}
	if managed.Telos.Accounting.State != "stale" || managed.Telos.Accounting.AsOf == nil {
		t.Fatalf("lost accounting freshness: %+v", managed.Telos.Accounting)
	}
	for i, want := range []struct {
		spend, estimate int64
		kind            string
	}{
		{1250000, 3400000, "api_cost"}, {800000, 6400000, "api_equivalent"},
	} {
		cost := sessions[i+1].Cost
		if cost == nil || *cost.Telos.SpendMicroUSD != want.spend || cost.Telos.Categories.InferenceMicroUSD != 0 {
			t.Fatalf("external cost must exclude managed inference: %+v", cost)
		}
		if cost.ExternalInference.EstimateKind != want.kind || *cost.ExternalInference.EstimatedCostMicroUSD != want.estimate {
			t.Fatalf("external estimate: %+v", cost.ExternalInference)
		}
	}
	if sessions[3].Cost.Telos.SpendMicroUSD != nil || sessions[3].Cost.Telos.Categories != nil {
		t.Fatalf("missing billing row became zero spend: %+v", sessions[3].Cost)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests["/api/billing/deployments"] != 1 || len(requests) != 3 {
		t.Fatalf("requests: %v", requests)
	}
}

func TestSessionCostsUnavailableDoesNotLoseInferenceConfiguration(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			sessions := []SessionRecord{{ID: "external", Name: "My goal", Inference: &InferenceSummary{Source: "subscription", Provider: "xai-grok"}}}
			NewClient(server.URL, "").PopulateSessionCosts(sessions)
			cost := sessions[0].Cost
			if sessions[0].Name != "My goal" || cost == nil || cost.Telos.SpendMicroUSD != nil || cost.ExternalInference == nil || cost.ExternalInference.EstimatedCostMicroUSD != nil {
				t.Fatalf("unavailable data lost or became zero: %+v", sessions[0])
			}
			encoded, err := json.Marshal(cost)
			if err != nil || !strings.Contains(string(encoded), `"estimated_cost_micro_usd":null`) || !strings.Contains(string(encoded), `"spend_micro_usd":null`) {
				t.Fatalf("unavailable JSON: %s, %v", encoded, err)
			}
		})
	}
}

func TestSessionCostsReadOlderListInferenceFromDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/deployments/external":
			fmt.Fprint(w, `{"id":"external","name":"detail name","inference":{"source":"byok","provider":"anthropic"}}`)
		case "/api/deployments/external/inference-usage":
			fmt.Fprint(w, `{"estimated_cost_micro_usd":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	sessions := []SessionRecord{{ID: "external", Name: "list name"}, {ID: "unknown"}}
	NewClient(server.URL, "").PopulateSessionCosts(sessions)
	if sessions[0].Name != "list name" || sessions[0].Cost.ExternalInference.Provider != "anthropic" || *sessions[0].Cost.ExternalInference.EstimatedCostMicroUSD != 0 {
		t.Fatalf("legacy list enrichment: %+v", sessions[0])
	}
	if sessions[1].Cost != nil {
		t.Fatalf("unknown inference was classified as managed: %+v", sessions[1].Cost)
	}
}

func TestSessionCostBreakdownReconcilesLikeWeb(t *testing.T) {
	for _, test := range []struct {
		name          string
		row           deploymentSpend
		external      bool
		wantAmount    int64
		wantReversed  int64
		wantBreakdown bool
	}{
		{"managed adjustment", deploymentSpend{GrossMicroUSD: 2000000, NetMicroUSD: costMicroUSD(1500000), ReversalsMicroUSD: 500000, InferenceMicroUSD: 1000000, ComputeMicroUSD: 1000000}, false, 1500000, 500000, true},
		{"external adjustment", deploymentSpend{NetMicroUSD: costMicroUSD(9500000), NonInferenceNetMicroUSD: costMicroUSD(1500000), InferenceMicroUSD: 8000000, ComputeMicroUSD: 2000000}, true, 1500000, 500000, true},
		{"inconsistent categories", deploymentSpend{GrossMicroUSD: 2000000, NetMicroUSD: costMicroUSD(2000000), ComputeMicroUSD: 1000000}, false, 2000000, 0, false},
		{"legacy external total", deploymentSpend{NetMicroUSD: costMicroUSD(1500000), ComputeMicroUSD: 2000000}, true, 1500000, 0, false},
		{"known zero", deploymentSpend{NetMicroUSD: costMicroUSD(0)}, false, 0, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cost := test.row.cost(test.external)
			if cost.SpendMicroUSD == nil || *cost.SpendMicroUSD != test.wantAmount || cost.ReversedMicroUSD != test.wantReversed || (cost.Categories != nil) != test.wantBreakdown {
				t.Fatalf("cost: %+v", cost)
			}
			if cost.Categories != nil {
				categories := cost.Categories
				if max(categories.InferenceMicroUSD+categories.ComputeMicroUSD+categories.StorageMicroUSD-cost.ReversedMicroUSD, 0) != *cost.SpendMicroUSD {
					t.Fatal("breakdown does not reconcile")
				}
			}
		})
	}
}

func TestSessionCostEnrichmentHasSharedDeadlineAndBoundedConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, maximum atomic.Int32
		client := NewClient("https://cloud.test", "")
		client.HTTP.Transport = readRetryTransport(func(r *http.Request) (*http.Response, error) {
			n := active.Add(1)
			for previous := maximum.Load(); n > previous; previous = maximum.Load() {
				if maximum.CompareAndSwap(previous, n) {
					break
				}
			}
			defer active.Add(-1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		sessions := make([]SessionRecord, 20)
		for i := range sessions {
			sessions[i] = SessionRecord{ID: fmt.Sprint(i), Inference: &InferenceSummary{Source: "byok", Provider: "anthropic"}}
		}
		start := time.Now()
		client.PopulateSessionCosts(sessions)
		if elapsed := time.Since(start); elapsed != 10*time.Second {
			t.Fatalf("enrichment deadline: %s", elapsed)
		}
		if maximum.Load() > 5 || active.Load() != 0 {
			t.Fatalf("unbounded or leaked requests: max=%d active=%d", maximum.Load(), active.Load())
		}
		for _, session := range sessions {
			if session.Cost == nil || session.Cost.ExternalInference == nil || session.Cost.ExternalInference.EstimatedCostMicroUSD != nil {
				t.Fatalf("timeout lost external identity: %+v", session)
			}
		}
	})
}
