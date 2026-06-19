package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/sessionapi"
)

func cmdReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	role := fs.String("role", "", "Agent role for a single session.jsonl path: prover or verifier")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos replay SESSION_OR_SESSION_JSONL [--role prover|verifier] [--json]")
		os.Exit(1)
	}
	reports, err := replayTarget(fs.Arg(0), *role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(reports)
		return
	}
	printReplayReports(os.Stdout, reports)
}

type replayReport struct {
	Role   string                       `json:"role"`
	Report executor.SessionReplayReport `json:"report"`
}

func replayTarget(target, role string) ([]replayReport, error) {
	if path, ok := existingReplayPath(target); ok {
		inferred := strings.TrimSpace(role)
		if inferred == "" {
			inferred = inferReplayRole(path)
		}
		if inferred == "" {
			return nil, fmt.Errorf("--role is required when replaying a session log path that does not include a prover/verifier turn directory")
		}
		report, err := executor.ReplaySessionLog(path, inferred)
		if err != nil {
			return nil, err
		}
		return []replayReport{{Role: inferred, Report: report}}, nil
	}

	session, err := store().Get(target)
	if err != nil {
		return nil, localSessionNotFoundError(target)
	}
	return replayLocalSession(session)
}

func existingReplayPath(target string) (string, bool) {
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return "", false
	}
	abs, _ := filepath.Abs(target)
	return abs, true
}

func replayLocalSession(session *sessionapi.Session) ([]replayReport, error) {
	if session == nil || session.SessionDir == nil || *session.SessionDir == "" {
		return nil, fmt.Errorf("session has no local session directory")
	}
	pattern := filepath.Join(*session.SessionDir, "specs", "*", "turns", "*-*", "session.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no turn session logs found for %s", session.SessionID)
	}
	reports := make([]replayReport, 0, len(paths))
	for _, path := range paths {
		role := inferReplayRole(path)
		if role == "" {
			return nil, fmt.Errorf("cannot infer role from %s", path)
		}
		report, err := executor.ReplaySessionLog(path, role)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		reports = append(reports, replayReport{Role: role, Report: report})
	}
	return reports, nil
}

func inferReplayRole(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	switch {
	case strings.HasSuffix(dir, "-prover"):
		return "prover"
	case strings.HasSuffix(dir, "-verifier"):
		return "verifier"
	default:
		return ""
	}
}

func printReplayReports(out io.Writer, reports []replayReport) {
	fmt.Fprintln(out, "Replay")
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ROLE\tPROTOCOL\tERROR\tEVENTS\tMODEL\tTOOLS\tTOOL_ERR\tEXIT\tTRUNC\tMISMATCH\tPATH")
	for _, item := range reports {
		protocol := "ok"
		errText := "-"
		if !item.Report.ProtocolOK {
			protocol = "fail"
			errText = item.Report.ProtocolError
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d/%d\t%d/%d\t%d\t%d\t%d\t%d\t%s\n",
			item.Role,
			protocol,
			errText,
			item.Report.Events,
			item.Report.ModelRequests,
			item.Report.ModelResponses,
			item.Report.ToolCalls,
			item.Report.ToolResults,
			item.Report.ToolErrors,
			item.Report.ToolNonzeroExits,
			item.Report.ToolTruncated,
			item.Report.UnmatchedToolCalls+item.Report.UnmatchedToolResults,
			item.Report.Path,
		)
	}
	_ = w.Flush()
}
