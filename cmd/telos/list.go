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
		fmt.Fprintln(os.Stderr, "error: --environments is no longer supported; telos list shows deployments")
		os.Exit(1)
	}

	var sessions []sessionapi.Session
	rootScoped := false
	fetchLimit := 0
	if *wide {
		fetchLimit = *limit
	}

	if !*localOnly && *env == "" {
		rootSessions, handled, err := rootListSessions(*limit)
		if handled {
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			sessions = append(sessions, rootSessions...)
			rootScoped = true
		} else if config.IsConfigured() {
			listDeployments(*jsonOut, *limit, *wide)
			return
		} else {
			sessions = append(sessions, listLocalAndConfiguredCloudSessions(*localOnly, *cloudOnly, *env, fetchLimit, *wide)...)
		}
	} else {
		sessions = append(sessions, listLocalAndConfiguredCloudSessions(*localOnly, *cloudOnly, *env, fetchLimit, *wide)...)
	}

	effectiveWide := *wide || rootScoped
	visible := visibleListSessions(sessions, effectiveWide)
	visible = limitListSessions(visible, *limit)
	if *jsonOut {
		printJSON(sessionapi.SessionListResponse{Sessions: sessionapi.SessionListItems(visible)})
		return
	}

	if len(visible) == 0 {
		if !effectiveWide && len(sessions) > 0 {
			fmt.Println("no top-level sessions (use --wide for child sessions)")
		} else {
			fmt.Println("no sessions")
		}
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if effectiveWide {
		fmt.Fprintln(w, "NAME\tPLATFORM\tSTATUS\tPARENT\tCOST\tSESSION")
	} else {
		fmt.Fprintln(w, "NAME\tPLATFORM\tSTATUS\tCOST\tSESSION")
	}
	for _, sess := range visible {
		row := displayRow(sess)
		if effectiveWide {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				row.Name,
				row.Platform,
				row.Status,
				row.Parent,
				row.Cost,
				row.Session,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			row.Platform,
			row.Status,
			row.Cost,
			row.Session,
		)
	}
	_ = w.Flush()
}

func rootListSessions(limit int) ([]sessionapi.Session, bool, error) {
	if sessionID, ok := localRootSessionID(); ok {
		sessions, err := store().List()
		if err != nil {
			return nil, true, fmt.Errorf("local root session list failed: %w", err)
		}
		return sessionTreeForRoot(sessions, sessionID), true, nil
	}

	ctx, ok := rootSessionContext()
	if !ok {
		return nil, false, nil
	}
	sessions, err := cloud.NewClient(ctx.endpoint, ctx.token).ListSessions(limit, true)
	if err != nil {
		return nil, true, fmt.Errorf("root session list failed: %w", err)
	}
	return sessions, true, nil
}

func sessionTreeForRoot(sessions []sessionapi.Session, rootID string) []sessionapi.Session {
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

func listLocalAndConfiguredCloudSessions(localOnly bool, cloudOnly bool, envID string, limit int, includeChildren bool) []sessionapi.Session {
	var sessions []sessionapi.Session
	if !cloudOnly {
		local, err := store().List()
		if err == nil {
			sessions = append(sessions, local...)
		}
	}
	if !localOnly && (cloudOnly || envID != "" || config.IsConfigured()) {
		cloudSessions, err := listCloudSessions(envID, limit, includeChildren)
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
		if session.ParentSessionID == nil || *session.ParentSessionID == "" {
			visible = append(visible, session)
		}
	}
	return visible
}

func limitListSessions(sessions []sessionapi.Session, limit int) []sessionapi.Session {
	if limit > 0 && len(sessions) > limit {
		return sessions[:limit]
	}
	return sessions
}

func listDeployments(jsonOut bool, limit int, wide bool) {
	control, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	deployments, err := control.ListDeployments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	deployments = limitDeployments(deployments, limit)
	if jsonOut {
		printJSON(cloud.DeploymentListResponse{Deployments: deployments})
		return
	}
	if len(deployments) == 0 {
		fmt.Println("no deployments")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if wide {
		fmt.Fprintln(w, "NAME\tSTATUS\tPACKAGE\tSERVICE\tDASHBOARD\tDEPLOYMENT")
	} else {
		fmt.Fprintln(w, "NAME\tSTATUS\tSERVICE\tDASHBOARD\tDEPLOYMENT")
	}
	for _, deployment := range deployments {
		serviceURL := optionalDeploymentString(deployment.ServiceURL)
		dashboardURL := optionalDeploymentString(deployment.DashboardURL)
		if wide {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				deployment.Name,
				deployment.State,
				deployment.PackageRef,
				serviceURL,
				dashboardURL,
				deployment.ID,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			deployment.Name,
			deployment.State,
			serviceURL,
			dashboardURL,
			deployment.ID,
		)
	}
	_ = w.Flush()
}

func limitDeployments(deployments []cloud.DeploymentRecord, limit int) []cloud.DeploymentRecord {
	if limit > 0 && len(deployments) > limit {
		return deployments[:limit]
	}
	return deployments
}

func optionalDeploymentString(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
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

func sessionLineage(sess sessionapi.Session) string {
	if isTopLevelSession(sess) {
		return "root"
	}
	return "child"
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

func sessionTurn(sess sessionapi.Session) string {
	if sess.CurrentRound == nil || sess.CurrentRole == nil || *sess.CurrentRole == "" {
		return "-"
	}
	return fmt.Sprintf("%s#%d", *sess.CurrentRole, *sess.CurrentRound)
}

func sessionArtifact(sess sessionapi.Session) string {
	if url := sessionServiceURL(sess); url != "" {
		return url
	}
	return "-"
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
