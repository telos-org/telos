package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- list ---------------------------------------------------------------------

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	env := fs.String("env", "", "Cloud environment")
	limit := fs.Int("limit", 0, "Limit results")
	wide := fs.Bool("wide", false, "Wide output")
	environments := fs.Bool("environments", false, "List cloud environments")
	localOnly := fs.Bool("local", false, "Local sessions only")
	cloudOnly := fs.Bool("cloud", false, "Cloud sessions only")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if *environments {
		listEnvironments(*jsonOut)
		return
	}

	var sessions []sessionapi.Session
	controllerScoped := false

	if !*localOnly && *env == "" {
		controllerSessions, handled, err := controllerListSessions(*limit)
		if handled {
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			sessions = append(sessions, controllerSessions...)
			controllerScoped = true
		} else {
			sessions = append(sessions, listLocalAndConfiguredCloudSessions(*localOnly, *cloudOnly, *env, *limit)...)
		}
	} else {
		sessions = append(sessions, listLocalAndConfiguredCloudSessions(*localOnly, *cloudOnly, *env, *limit)...)
	}

	if *limit > 0 && len(sessions) > *limit {
		sessions = sessions[:*limit]
	}

	effectiveWide := *wide || controllerScoped
	visible := visibleListSessions(sessions, effectiveWide)
	if *jsonOut {
		printJSON(sessionapi.SessionListResponse{Sessions: visible})
		return
	}

	if len(visible) == 0 {
		if !effectiveWide && len(sessions) > 0 {
			fmt.Println("no active sessions (use --wide for history)")
		} else {
			fmt.Println("no sessions")
		}
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if effectiveWide {
		fmt.Fprintln(w, "NAME\tKIND\tSTATUS\tRESULT\tRUNTIME\tARTIFACT\tPARENT\tCOST\tSESSION")
	} else {
		fmt.Fprintln(w, "NAME\tSTATUS\tRESULT\tARTIFACT\tSESSION")
	}
	for _, sess := range visible {
		if effectiveWide {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sessionName(sess),
				sessionKind(sess),
				sess.Status,
				sessionResult(sess),
				sess.Runtime,
				sessionArtifact(sess),
				sessionParent(sess),
				sessionCost(sess),
				sess.SessionID,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			sessionName(sess),
			sess.Status,
			sessionResult(sess),
			sessionArtifact(sess),
			sess.SessionID,
		)
	}
	_ = w.Flush()
}

func controllerListSessions(limit int) ([]sessionapi.Session, bool, error) {
	if sessionID, ok := localControllerSessionID(); ok {
		sessions, err := store().List()
		if err != nil {
			return nil, true, fmt.Errorf("local controller session list failed: %w", err)
		}
		return controllerSessionTree(sessions, sessionID), true, nil
	}

	ctx, ok := controllerSessionContext()
	if !ok {
		return nil, false, nil
	}
	sessions, err := cloud.NewClient(ctx.endpoint, ctx.token).ListSessions(limit)
	if err != nil {
		return nil, true, fmt.Errorf("controller session list failed: %w", err)
	}
	return sessions, true, nil
}

func controllerSessionTree(sessions []sessionapi.Session, rootID string) []sessionapi.Session {
	byID := make(map[string]sessionapi.Session, len(sessions))
	childrenByParent := make(map[string][]sessionapi.Session)
	for _, session := range sessions {
		byID[session.SessionID] = session
		if session.ParentSessionID != nil && *session.ParentSessionID != "" {
			childrenByParent[*session.ParentSessionID] = append(childrenByParent[*session.ParentSessionID], session)
		}
	}

	root, ok := byID[rootID]
	if !ok {
		return nil
	}

	out := []sessionapi.Session{root}
	seen := map[string]bool{rootID: true}
	queue := []string{rootID}
	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[parentID] {
			if seen[child.SessionID] {
				continue
			}
			seen[child.SessionID] = true
			out = append(out, child)
			queue = append(queue, child.SessionID)
		}
	}
	return out
}

func listLocalAndConfiguredCloudSessions(localOnly bool, cloudOnly bool, envID string, limit int) []sessionapi.Session {
	var sessions []sessionapi.Session
	if !cloudOnly {
		local, err := store().List()
		if err == nil {
			sessions = append(sessions, local...)
		}
	}
	if !localOnly && (cloudOnly || envID != "" || config.IsConfigured()) {
		cloudSessions, err := listCloudSessions(envID, limit)
		if err != nil && (cloudOnly || envID != "") {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err == nil {
			sessions = append(sessions, cloudSessions...)
		}
	}
	return sessions
}

func visibleListSessions(sessions []sessionapi.Session, wide bool) []sessionapi.Session {
	if wide {
		return sessions
	}
	visible := make([]sessionapi.Session, 0, len(sessions))
	for _, session := range sessions {
		if (session.ParentSessionID == nil || *session.ParentSessionID == "") &&
			sessionVisibleByDefault(session) {
			visible = append(visible, session)
		}
	}
	return visible
}

func sessionVisibleByDefault(session sessionapi.Session) bool {
	switch session.Status {
	case sessionapi.StatusPending, sessionapi.StatusRunning, sessionapi.StatusScheduled:
		return true
	default:
		return false
	}
}

func listEnvironments(jsonOut bool) {
	control, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	envs, err := control.ListEnvironments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{"environments": environmentsOutput(envs)})
		return
	}
	if len(envs) == 0 {
		fmt.Println("no environments")
		return
	}
	for _, env := range envs {
		access := "-"
		if _, ok := config.EnvironmentAccessByID(env.ID); ok {
			access = "local"
		} else if env.HasRecoverable {
			access = "recoverable"
		}
		fmt.Printf("%-16s %-14s %-36s %s\n", env.ID, env.State, env.Handle, access)
	}
}

func sessionName(sess sessionapi.Session) string {
	if sess.SpecName != nil && *sess.SpecName != "" {
		return *sess.SpecName
	}
	return "-"
}

func sessionKind(sess sessionapi.Session) string {
	if sess.SessionKind != nil && *sess.SessionKind != "" {
		return string(*sess.SessionKind)
	}
	return "-"
}

func sessionParent(sess sessionapi.Session) string {
	if sess.ParentSessionID != nil && *sess.ParentSessionID != "" {
		return *sess.ParentSessionID
	}
	return "-"
}

func sessionCost(sess sessionapi.Session) string {
	if sess.TotalCostUSD == nil {
		return "-"
	}
	return fmt.Sprintf("$%.2f", *sess.TotalCostUSD)
}

func sessionArtifact(sess sessionapi.Session) string {
	if sess.ArtifactURI != nil && *sess.ArtifactURI != "" {
		return *sess.ArtifactURI
	}
	return "-"
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
