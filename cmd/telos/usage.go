package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/sessionapi"
)

// -- usage --------------------------------------------------------------------

type sessionUsage struct {
	Name                string   `json:"spec_name"`
	SessionID           string   `json:"session_id"`
	Status              string   `json:"status"`
	InputTokens         *int     `json:"total_input_tokens,omitempty"`
	OutputTokens        *int     `json:"total_output_tokens,omitempty"`
	CacheReadTokens     *int     `json:"total_cache_read_tokens,omitempty"`
	CacheCreationTokens *int     `json:"total_cache_creation_tokens,omitempty"`
	CostUSD             *float64 `json:"total_cost_usd,omitempty"`
}

type usageTotals struct {
	Sessions            int     `json:"sessions"`
	InputTokens         int     `json:"total_input_tokens"`
	OutputTokens        int     `json:"total_output_tokens"`
	CacheReadTokens     int     `json:"total_cache_read_tokens"`
	CacheCreationTokens int     `json:"total_cache_creation_tokens"`
	CostUSD             float64 `json:"total_cost_usd"`
}

type usageReport struct {
	Sessions []sessionUsage `json:"sessions"`
	Totals   usageTotals    `json:"totals"`
}

func cmdUsage(args []string) {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	limit := fs.Int("limit", 0, "Limit results")
	localOnly := fs.Bool("local", false, "Local sessions only")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	// Usage lives on runtime sessions, not cloud deployments: use the root
	// session tree when running delegated, otherwise the local store.
	var sessions []sessionapi.Session
	if !*localOnly {
		rootSessions, handled, err := rootListSessions(*limit)
		if handled {
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			sessions = rootSessions
		} else {
			sessions = listLocalSessions()
		}
	} else {
		sessions = listLocalSessions()
	}
	// Child sessions carry their own turn stats, so they stay in the report;
	// hiding them would undercount the totals.
	sessions = limitListSessions(sessions, *limit)

	report := usageReportFromSessions(sessions)
	if *jsonOut {
		printJSON(report)
		return
	}
	if len(report.Sessions) == 0 {
		fmt.Println("no sessions")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tINPUT\tOUTPUT\tCACHE-R\tCACHE-W\tCOST\tSESSION")
	for _, row := range report.Sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			row.Status,
			usageTokens(row.InputTokens),
			usageTokens(row.OutputTokens),
			usageTokens(row.CacheReadTokens),
			usageTokens(row.CacheCreationTokens),
			formatDetailCost(row.CostUSD),
			row.SessionID,
		)
	}
	totals := report.Totals
	fmt.Fprintf(w, "TOTAL\t%d sessions\t%d\t%d\t%d\t%d\t%s\t\n",
		totals.Sessions,
		totals.InputTokens,
		totals.OutputTokens,
		totals.CacheReadTokens,
		totals.CacheCreationTokens,
		usageTotalCost(report),
	)
	_ = w.Flush()
}

func usageReportFromSessions(sessions []sessionapi.Session) usageReport {
	report := usageReport{Sessions: make([]sessionUsage, 0, len(sessions))}
	for _, sess := range sessions {
		row := sessionUsage{
			Name:                sessionName(sess),
			SessionID:           sess.SessionID,
			Status:              sessionDisplayStatus(sess),
			InputTokens:         sess.TotalInputTokens,
			OutputTokens:        sess.TotalOutputTokens,
			CacheReadTokens:     sess.TotalCacheReadTokens,
			CacheCreationTokens: sess.TotalCacheCreateTokens,
			CostUSD:             sess.TotalCostUSD,
		}
		report.Sessions = append(report.Sessions, row)
		report.Totals.Sessions++
		report.Totals.InputTokens += intOrZero(row.InputTokens)
		report.Totals.OutputTokens += intOrZero(row.OutputTokens)
		report.Totals.CacheReadTokens += intOrZero(row.CacheReadTokens)
		report.Totals.CacheCreationTokens += intOrZero(row.CacheCreationTokens)
		if row.CostUSD != nil {
			report.Totals.CostUSD += *row.CostUSD
		}
	}
	return report
}

func usageTokens(value *int) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}

func usageTotalCost(report usageReport) string {
	for _, row := range report.Sessions {
		if row.CostUSD != nil {
			return fmt.Sprintf("$%.4f", report.Totals.CostUSD)
		}
	}
	return "-"
}

func intOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
