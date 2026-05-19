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

	// Local sessions
	if !*cloudOnly {
		s := store()
		local, err := s.List()
		if err == nil {
			sessions = append(sessions, local...)
		}
	}

	// Cloud sessions
	if !*localOnly && (*cloudOnly || *env != "" || config.IsConfigured()) {
		cloudSessions, err := listCloudSessions(*env, *limit)
		if err != nil && (*cloudOnly || *env != "") {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err == nil {
			sessions = append(sessions, cloudSessions...)
		}
	}

	if *limit > 0 && len(sessions) > *limit {
		sessions = sessions[:*limit]
	}

	visible := visibleListSessions(sessions, *wide)
	if *jsonOut {
		printJSON(sessionapi.SessionListResponse{Sessions: visible})
		return
	}

	if len(visible) == 0 {
		fmt.Println("no sessions")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if *wide {
		fmt.Fprintln(w, "NAME\tKIND\tSTATUS\tRESULT\tRUNTIME\tARTIFACT\tPARENT\tCOST\tSESSION")
	} else {
		fmt.Fprintln(w, "NAME\tSTATUS\tRESULT\tARTIFACT\tSESSION")
	}
	for _, sess := range visible {
		if *wide {
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
