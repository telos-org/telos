package main

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/runtimeclient"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- list ---------------------------------------------------------------------

func cmdList(args []string) {
	fs := newCommandFlagSet("list", "telos list [goals|proposals|comments] [--filter KEY=VALUE,...] [flags]")
	filter := fs.String("filter", "", "Show only results matching every KEY=VALUE, such as status=open,author=me")
	limit := fs.Int("limit", 0, "Limit results")
	wide := fs.Bool("wide", false, "Wide output for Goals")
	localOnly := fs.Bool("local", false, "Local Goals only")
	cloudOnly := fs.Bool("cloud", false, "Cloud Goals only")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	if fs.NArg() > 1 {
		requireArgCount(fs, 1, "at most one of goals, proposals, or comments")
	}
	kind := cmp.Or(fs.Arg(0), "goals")
	filters, err := parseListFilter(kind, *filter)
	if err != nil {
		exitUsage("%v", err)
	}
	if kind != "goals" && flagNamesSet(fs, "wide", "local", "cloud") {
		exitUsage("--wide, --local, and --cloud apply only to Goals")
	}
	if *localOnly && *cloudOnly {
		fmt.Fprintln(os.Stderr, "error: --local and --cloud are mutually exclusive")
		os.Exit(2)
	}
	if *limit < 0 {
		fmt.Fprintln(os.Stderr, "error: --limit must be non-negative")
		os.Exit(2)
	}
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if contextOverride != "" && *localOnly {
		fmt.Fprintln(os.Stderr, "error: --context cannot be used with --local")
		os.Exit(2)
	}
	switch kind {
	case "proposals":
		listProposals(cloudClient(contextOverride), filters, *limit, *jsonOut)
		return
	case "comments":
		listComments(cloudClient(contextOverride), filters, *limit, *jsonOut)
		return
	}
	status := filters.Get("status")

	if *cloudOnly || contextOverride != "" {
		listCloudSessions(contextOverride, *jsonOut, *limit, *wide, status)
		return
	}

	var sessions []sessionapi.Session
	rootScoped := false
	signedOut := false

	if !*localOnly {
		rootSessions, handled, err := rootListSessions(*limit)
		if handled {
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			sessions = append(sessions, rootSessions...)
			rootScoped = true
		} else {
			cloudConfigured, err := config.IsConfigured()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			if cloudConfigured {
				listCloudSessions(contextOverride, *jsonOut, *limit, *wide, status)
				return
			}
			signedOut = true
			sessions = append(sessions, listLocalSessions()...)
		}
	} else {
		sessions = append(sessions, listLocalSessions()...)
	}

	effectiveWide := *wide || rootScoped
	visible := visibleListSessions(sessions, effectiveWide)
	visible = withStatus(visible, status, func(session sessionapi.Session) string { return displayRow(session).Status })
	visible = limited(visible, *limit)
	if *jsonOut {
		printJSON(map[string]any{"goals": goalJSONList(sessionapi.SessionListItems(visible))})
		return
	}

	if len(visible) == 0 {
		switch {
		case !effectiveWide && len(sessions) > 0:
			fmt.Println("no top-level Goals (use --wide for child Goals)")
		case signedOut:
			fmt.Println("Not signed in. Run `telos login` to see your Goals.")
		default:
			fmt.Println("no Goals")
		}
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if effectiveWide {
		fmt.Fprintln(w, "NAME\tTARGET\tSTATUS\tPARENT\tCOST\tID")
	} else {
		fmt.Fprintln(w, "NAME\tSTATUS\tID")
	}
	for _, sess := range visible {
		row := displayRow(sess)
		if effectiveWide {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				row.Name,
				row.Target,
				row.Status,
				row.Parent,
				row.Cost,
				row.Session,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			row.Name,
			row.Status,
			row.Session,
		)
	}
	_ = w.Flush()
}

func rootListSessions(limit int) ([]sessionapi.Session, bool, error) {
	ctx, ok := rootSessionContext()
	if !ok {
		return nil, false, nil
	}
	sessions, err := runtimeclient.New(ctx.endpoint, ctx.token).ListSessions(limit, true)
	if err != nil {
		return nil, true, fmt.Errorf("root session list failed: %w", err)
	}
	return sessions, true, nil
}

func listLocalSessions() []sessionapi.Session {
	sessions, err := store().List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: list local Goals: %v\n", err)
		os.Exit(1)
	}
	return sessions
}

func visibleListSessions(sessions []sessionapi.Session, wide bool) []sessionapi.Session {
	if wide {
		return sessions
	}
	visible := make([]sessionapi.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ParentSessionID == nil || *session.ParentSessionID == "" {
			visible = append(visible, session)
		}
	}
	return visible
}

// limited returns the first limit items, or all of them when limit is 0.
func limited[T any](items []T, limit int) []T {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// withStatus keeps the items whose status is one of the comma-separated
// statuses, or all when it's "".
func withStatus[T any](items []T, status string, statusOf func(T) string) []T {
	if status == "" {
		return items
	}
	kept := make([]T, 0, len(items))
	for _, item := range items {
		if slices.Contains(strings.Split(status, ","), statusOf(item)) {
			kept = append(kept, item)
		}
	}
	return kept
}

func listCloudSessions(contextOverride string, jsonOut bool, limit int, wide bool, status string) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	list := control.ListSessions
	if wide {
		list = control.ListSessionsWithBilling
	}
	cloudSessions, err := list()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cloudSessions = withStatus(cloudSessions, status, cloudSessionDisplayStatus)
	cloudSessions = limited(cloudSessions, limit)
	var costs []*goalCost
	if wide {
		costs = goalCosts(control, cloudSessions)
	}
	if jsonOut {
		sessions := make([]cloudSessionJSON, 0, len(cloudSessions))
		for index := range cloudSessions {
			record := newCloudSessionJSON(&cloudSessions[index])
			if costs != nil {
				record.Cost = costs[index]
			}
			sessions = append(sessions, record)
		}
		printJSON(map[string]any{
			"context": control.ContextName(),
			"goals":   sessions,
		})
		return
	}
	if len(cloudSessions) == 0 {
		fmt.Println("no Goals")
		return
	}
	if wide {
		printSummaryField(os.Stdout, "Context", control.ContextName())
		fmt.Println()
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if wide {
		fmt.Fprintln(w, "NAME\tSTATUS\tREVISION\tSERVICE\tCLOUD\tINFERENCE\tID")
	} else {
		fmt.Fprintln(w, "NAME\tSTATUS\tID")
	}
	for index, session := range cloudSessions {
		serviceURL := optionalSessionString(session.ServiceURL)
		if wide {
			cloudSpend, inference := costCells(costs[index])
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				session.Name,
				cloudSessionStatusCell(session),
				shortRevision(session.PackageDigest),
				serviceURL,
				cloudSpend,
				inference,
				session.ID,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			session.Name,
			cloudSessionStatusCell(session),
			session.ID,
		)
	}
	_ = w.Flush()
}

func optionalSessionString(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
}

func sessionName(sess sessionapi.Session) string {
	if sess.SpecName != nil && *sess.SpecName != "" {
		return *sess.SpecName
	}
	return "-"
}

func sessionParent(sess sessionapi.Session) string {
	if sess.ParentSessionID != nil && *sess.ParentSessionID != "" {
		return *sess.ParentSessionID
	}
	return "-"
}

func isTopLevelSession(sess sessionapi.Session) bool {
	return sess.ParentSessionID == nil || *sess.ParentSessionID == ""
}

func sessionCost(sess sessionapi.Session) string {
	if sess.TotalCostUSD == nil {
		return "-"
	}
	return fmt.Sprintf("$%.2f", *sess.TotalCostUSD)
}

func sessionServiceURL(sess sessionapi.Session) string {
	if sess.ServiceURL != nil && *sess.ServiceURL != "" {
		return *sess.ServiceURL
	}
	return ""
}

func sessionResult(sess sessionapi.Session) string {
	if sess.Result != nil && *sess.Result != "" {
		return *sess.Result
	}
	if result := latestEpochString(sess, "result"); result != "" {
		return result
	}
	if sess.Status.IsTerminal() {
		return string(sess.Status)
	}
	return "active"
}

func latestEpochString(sess sessionapi.Session, key string) string {
	if len(sess.Epochs) == 0 {
		return ""
	}
	value, ok := sess.Epochs[len(sess.Epochs)-1][key]
	if !ok || value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprint(value)
}
