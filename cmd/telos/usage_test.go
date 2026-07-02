package main

import (
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestUsageReportFromSessionsSumsTotals(t *testing.T) {
	name := "hello-service"
	inA, outA, cacheReadA, cacheWriteA, costA := 1200, 300, 5000, 800, 0.25
	inB, outB := 100, 50
	sessions := []sessionapi.Session{
		{
			SessionID:              "sess_a",
			SpecName:               &name,
			Status:                 sessionapi.StatusCompleted,
			TotalInputTokens:       &inA,
			TotalOutputTokens:      &outA,
			TotalCacheReadTokens:   &cacheReadA,
			TotalCacheCreateTokens: &cacheWriteA,
			TotalCostUSD:           &costA,
		},
		{
			SessionID:         "sess_b",
			Status:            sessionapi.StatusRunning,
			TotalInputTokens:  &inB,
			TotalOutputTokens: &outB,
		},
	}

	report := usageReportFromSessions(sessions)
	if report.Totals.Sessions != 2 {
		t.Fatalf("total sessions: got %d, want 2", report.Totals.Sessions)
	}
	if report.Totals.InputTokens != 1300 || report.Totals.OutputTokens != 350 {
		t.Fatalf("token totals: got in=%d out=%d, want in=1300 out=350",
			report.Totals.InputTokens, report.Totals.OutputTokens)
	}
	if report.Totals.CacheReadTokens != 5000 || report.Totals.CacheCreationTokens != 800 {
		t.Fatalf("cache totals: got read=%d write=%d, want read=5000 write=800",
			report.Totals.CacheReadTokens, report.Totals.CacheCreationTokens)
	}
	if report.Totals.CostUSD != 0.25 {
		t.Fatalf("cost total: got %f, want 0.25", report.Totals.CostUSD)
	}
	if report.Sessions[0].Name != "hello-service" || report.Sessions[1].Name != "-" {
		t.Fatalf("row names: got %q, %q", report.Sessions[0].Name, report.Sessions[1].Name)
	}
}

func TestUsageReportKeepsMissingStatsUnset(t *testing.T) {
	report := usageReportFromSessions([]sessionapi.Session{
		{SessionID: "sess_new", Status: sessionapi.StatusPending},
	})
	row := report.Sessions[0]
	if row.InputTokens != nil || row.OutputTokens != nil || row.CostUSD != nil {
		t.Fatalf("missing stats should stay nil, got %#v", row)
	}
	if usageTokens(row.InputTokens) != "-" {
		t.Fatalf("missing tokens should render as -, got %q", usageTokens(row.InputTokens))
	}
	if usageTotalCost(report) != "-" {
		t.Fatalf("all-nil cost should render as -, got %q", usageTotalCost(report))
	}
}

func TestUsageTotalCostRendersWhenAnySessionHasCost(t *testing.T) {
	cost := 0.0
	report := usageReportFromSessions([]sessionapi.Session{
		{SessionID: "sess_free", TotalCostUSD: &cost},
	})
	if usageTotalCost(report) != "$0.0000" {
		t.Fatalf("zero cost should render, got %q", usageTotalCost(report))
	}
}

func TestCmdUsageEmptyLocalStore(t *testing.T) {
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", t.TempDir())

	out := captureStdout(t, func() {
		cmdUsage([]string{"--local"})
	})
	if !strings.Contains(out, "no sessions") {
		t.Fatalf("empty store output: got %q", out)
	}
}
