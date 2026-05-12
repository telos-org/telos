// Command telos is the Telos CLI and local runtime.
//
// Public commands:
//
//	telos plan SPEC.md [--json]
//	telos run SPEC.md [--workspace DIR] [--model MODEL] [--thinking EFFORT]
//	    [--max-rounds N] [--max-cost-usd USD] [--agent-timeout-sec SEC] [--json]
//	telos list [--env ENV] [--limit N] [--all] [--wide] [--local] [--hosted] [--json]
//	telos describe SESSION [--env ENV] [--json]
//	telos logs [-f] SESSION [--env ENV]
//	telos stop SESSION [--env ENV] [--json]
//	telos login [--endpoint URL] [--token TOKEN] [--no-prompt]
//	telos version
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos-go/internal/cli"
	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/hosted"
	"github.com/telos-org/telos-go/internal/sessionapi"
	"github.com/telos-org/telos-go/internal/spec"
)

// Version is set at build time.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "plan":
		cmdPlan(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "describe":
		cmdDescribe(os.Args[2:])
	case "logs":
		cmdLogs(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "login":
		cmdLogin(os.Args[2:])
	case "version":
		fmt.Println("telos " + Version)
	case "serve":
		cmdServe()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: telos <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  plan SPEC.md       Show compiled spec plan")
	fmt.Fprintln(os.Stderr, "  run SPEC.md        Create and run a session")
	fmt.Fprintln(os.Stderr, "  list               List sessions")
	fmt.Fprintln(os.Stderr, "  describe SESSION   Show session details")
	fmt.Fprintln(os.Stderr, "  logs SESSION       Show session transcript")
	fmt.Fprintln(os.Stderr, "  stop SESSION        Stop a running session")
	fmt.Fprintln(os.Stderr, "  login              Configure hosted access")
	fmt.Fprintln(os.Stderr, "  version            Show version")
}

// -- plan ---------------------------------------------------------------------

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos plan SPEC.md [--json]")
		os.Exit(1)
	}
	specPath := resolveSpecPath(fs.Arg(0))
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	plan := map[string]interface{}{
		"spec": map[string]interface{}{
			"name":         compiled.Environment.Name,
			"path":         specPath,
			"content_hash": compiled.ContentHash,
			"platform":     platform,
			"namespace":    compiled.Namespace,
			"lineage":      compiled.Lineage,
			"skills":       skillNames(compiled.Skills),
		},
		"session": map[string]interface{}{
			"kind":             "task",
			"interval_seconds": compiled.Environment.IntervalSeconds,
		},
		"target": map[string]interface{}{
			"mode":                "local",
			"will_create_session": true,
			"will_mutate":         false,
		},
		"user": map[string]interface{}{
			"status": "local",
			"label":  "local workspace",
			"detail": "no hosted auth required",
		},
	}

	if *jsonOut {
		printJSON(plan)
		return
	}

	fmt.Printf("Plan for %s\n\n", compiled.Environment.Name)
	fmt.Printf("Spec: %s\n", specPath)
	fmt.Printf("Platform: %s\n", platform)
	if platform != "local" {
		fmt.Printf("Namespace: %s\n", compiled.Namespace)
	}
	fmt.Printf("Content hash: %s\n", compiled.ContentHash)
	if len(compiled.Skills) > 0 {
		fmt.Printf("Skills: %s\n", strings.Join(skillNames(compiled.Skills), ", "))
	}
	fmt.Println("\nNo sessions or environments will be created.")
}

// -- run ----------------------------------------------------------------------

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	workspace := fs.String("workspace", "", "Workspace directory")
	env := fs.String("env", "", "Hosted environment ID")
	model := fs.String("model", "", "Model name")
	thinking := fs.String("thinking", "medium", "Thinking effort")
	maxRounds := fs.Int("max-rounds", 20, "Maximum PVG rounds")
	maxCostUSD := fs.Float64("max-cost-usd", 20.0, "Maximum cost in USD")
	agentTimeout := fs.Int("agent-timeout-sec", 1800, "Agent timeout in seconds")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos run SPEC.md [options]")
		os.Exit(1)
	}
	specPath := resolveSpecPath(fs.Arg(0))

	// Hosted mode
	if *env != "" {
		runHosted(specPath, *env, *jsonOut)
		return
	}

	// Check if spec is local
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if compiled.Environment.Platform != "local" && compiled.Environment.Platform != "" {
		// Try hosted
		if config.IsConfigured() {
			runHosted(specPath, "", *jsonOut)
			return
		}
		fmt.Fprintf(os.Stderr, "error: non-local spec requires hosted config; run `telos login` first\n")
		os.Exit(1)
	}

	cfg := cli.LocalRunConfig{
		Workspace:       *workspace,
		Model:           *model,
		Thinking:        *thinking,
		MaxRounds:       *maxRounds,
		MaxCostUSD:      maxCostUSD,
		AgentTimeoutSec: *agentTimeout,
	}

	session, err := cli.CreateLocalSession(specPath, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(map[string]interface{}{
			"session_id":  session.SessionID,
			"session_dir": session.SessionDir,
			"workspace":   session.Workspace,
			"spec_name":   session.SpecName,
			"status":      "running",
		})
	} else {
		fmt.Printf("session %s (%s)\n", session.SessionID, session.SpecName)
	}

	result, err := cli.RunLocalSession(session.SessionDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(map[string]interface{}{
			"session_id":  session.SessionID,
			"game_result": string(result.GameResult),
			"rounds":      result.Rounds,
			"cost_usd":    result.TotalCostUSD,
			"error":       result.Error,
		})
	} else {
		fmt.Printf("\n%s %s: %s (rounds=%d, cost=$%.2f)\n",
			session.SessionID, session.SpecName, result.GameResult,
			result.Rounds, result.TotalCostUSD)
		if result.Error != "" {
			fmt.Printf("error: %s\n", result.Error)
		}
	}
}

func runHosted(specPath, envID string, jsonOut bool) {
	client, env, err := hostedSessionClientForRun(envID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	specData, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	md := string(specData)
	req := sessionapi.SessionCreateRequest{
		SpecMarkdown: &md,
	}
	session, err := client.CreateSession(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"environment": env,
			"session":     session,
		})
	} else {
		fmt.Printf("session %s (status: %s)\n", session.SessionID, session.Status)
		if env != nil {
			fmt.Printf("environment %s %s\n", env.ID, env.Handle)
		}
	}
}

// -- list ---------------------------------------------------------------------

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	env := fs.String("env", "", "Hosted environment")
	limit := fs.Int("limit", 0, "Limit results")
	showAll := fs.Bool("all", false, "Show all sessions")
	wide := fs.Bool("wide", false, "Wide output")
	localOnly := fs.Bool("local", false, "Local sessions only")
	hostedOnly := fs.Bool("hosted", false, "Hosted sessions only")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	_ = showAll
	_ = wide

	var sessions []sessionapi.Session

	// Local sessions
	if !*hostedOnly {
		s := store()
		local, err := s.List()
		if err == nil {
			sessions = append(sessions, local...)
		}
	}

	// Hosted sessions
	if !*localOnly && (*hostedOnly || *env != "" || config.IsConfigured()) {
		hostedSessions, err := listHostedSessions(*env, *limit)
		if err != nil && (*hostedOnly || *env != "") {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err == nil {
			sessions = append(sessions, hostedSessions...)
		}
	}

	if *limit > 0 && len(sessions) > *limit {
		sessions = sessions[:*limit]
	}

	if *jsonOut {
		printJSON(sessionapi.SessionListResponse{Sessions: sessions})
		return
	}

	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return
	}
	for _, sess := range sessions {
		name := ""
		if sess.SpecName != nil {
			name = *sess.SpecName
		}
		cost := ""
		if sess.TotalCostUSD != nil {
			cost = fmt.Sprintf("$%.2f", *sess.TotalCostUSD)
		}
		fmt.Printf("%-40s %-12s %-20s %s\n", sess.SessionID, sess.Status, name, cost)
	}
}

// -- describe -----------------------------------------------------------------

func cmdDescribe(args []string) {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	env := fs.String("env", "", "Hosted environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos describe SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	session, err := getSessionFromAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(session)
		return
	}

	fmt.Printf("Session:  %s\n", session.SessionID)
	fmt.Printf("Status:   %s\n", session.Status)
	fmt.Printf("Runtime:  %s\n", session.Runtime)
	if session.SpecName != nil {
		fmt.Printf("Spec:     %s\n", *session.SpecName)
	}
	if session.CreatedAt != nil {
		fmt.Printf("Created:  %s\n", *session.CreatedAt)
	}
	if session.Result != nil {
		fmt.Printf("Result:   %s\n", *session.Result)
	}
	if session.Error != nil {
		fmt.Printf("Error:    %s\n", *session.Error)
	}
	if session.TotalCostUSD != nil {
		fmt.Printf("Cost:     $%.4f\n", *session.TotalCostUSD)
	}
	if session.RoundCount != nil {
		fmt.Printf("Rounds:   %d\n", *session.RoundCount)
	}
}

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "Follow transcript")
	env := fs.String("env", "", "Hosted environment")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] SESSION")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if *follow {
		followTranscript(sessionID, *env)
		return
	}

	text, err := getTranscriptFromAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(text)
}

func followTranscript(sessionID, envID string) {
	var lastLen int
	for {
		text, err := getTranscriptFromAnywhere(sessionID, envID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(text) > lastLen {
			fmt.Print(text[lastLen:])
			lastLen = len(text)
		}
		// Check if session is terminal
		sess, err := getSessionFromAnywhere(sessionID, envID)
		if err == nil && sess.Status.IsTerminal() {
			break
		}
		time.Sleep(2 * time.Second)
	}
}

// -- stop ---------------------------------------------------------------------

func cmdStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	env := fs.String("env", "", "Hosted environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos stop SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	session, err := stopSessionAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(session)
		return
	}
	fmt.Printf("session %s: %s\n", session.SessionID, session.Status)
}

// -- login --------------------------------------------------------------------

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	endpoint := fs.String("endpoint", hosted.DefaultAPIEndpoint, "API endpoint")
	token := fs.String("token", "", "API token")
	noPrompt := fs.Bool("no-prompt", false, "No interactive prompt")
	parseFlags(fs, args)

	ep := hosted.NormalizeEndpoint(*endpoint)
	tok := *token
	if tok == "" {
		tok = os.Getenv("TELOS_AUTH_TOKEN")
	}
	if tok == "" && !*noPrompt {
		fmt.Print("Telos API token: ")
		fmt.Scanln(&tok)
	}
	if tok == "" {
		fmt.Fprintln(os.Stderr, "error: token required")
		os.Exit(1)
	}

	cfg := config.LoadConfig()
	cfg.APIEndpoint = ep
	cfg.AuthToken = tok
	if err := config.SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(ep)
}

// -- serve (internal) ---------------------------------------------------------

func cmdServe() {
	addr := os.Getenv("TELOS_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	s := store()
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, s)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "telos sessions api listening on %s\n", ln.Addr())
	if err := http.Serve(ln, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

// -- Helpers ------------------------------------------------------------------

type boolFlag interface {
	IsBoolFlag() bool
}

func parseFlags(fs *flag.FlagSet, args []string) {
	ordered := reorderInterspersedFlags(fs, args)
	if err := fs.Parse(ordered); err != nil {
		os.Exit(2)
	}
}

func reorderInterspersedFlags(fs *flag.FlagSet, args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	afterDashDash := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if afterDashDash {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			afterDashDash = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)
		if strings.Contains(arg, "=") || flagIsBool(fs, arg) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positionals...)
}

func flagIsBool(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if idx := strings.Index(name, "="); idx >= 0 {
		name = name[:idx]
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(boolFlag)
	return ok && bf.IsBoolFlag()
}

func hostedSessionClientForRun(envID string) (*hosted.Client, *hosted.Environment, error) {
	if envID != "" {
		return hosted.NewEnvironmentClient(envID)
	}
	control, err := hosted.ControlClient()
	if err != nil {
		return nil, nil, err
	}
	env, err := control.CreateEnvironment()
	if err != nil {
		return nil, nil, err
	}
	if err := config.SaveEnvironmentAccessEntry(config.EnvironmentAccess{
		ID:        env.ID,
		EnvAPIKey: env.EnvAPIKey,
	}); err != nil {
		return nil, nil, err
	}
	if err := hosted.WaitForEnvironment(env.Handle, 15*time.Minute); err != nil {
		return nil, nil, err
	}
	return hosted.NewClient("https://"+env.Handle, env.EnvAPIKey), env, nil
}

func listHostedSessions(envID string, limit int) ([]sessionapi.Session, error) {
	var sessions []sessionapi.Session
	for _, client := range hostedSessionClients(envID) {
		found, err := client.ListSessions(limit)
		if err != nil {
			if envID != "" {
				return nil, err
			}
			continue
		}
		sessions = append(sessions, found...)
	}
	return sessions, nil
}

func hostedSessionClients(envID string) []*hosted.Client {
	if envID != "" {
		client, _, err := hosted.NewEnvironmentClient(envID)
		if err != nil {
			return nil
		}
		return []*hosted.Client{client}
	}

	control, err := hosted.ControlClient()
	if err != nil {
		return nil
	}
	envs, err := control.ListEnvironments()
	if err != nil {
		return nil
	}
	var clients []*hosted.Client
	for _, env := range envs {
		if env.Handle == "" {
			continue
		}
		access, ok := config.EnvironmentAccessByID(env.ID)
		if !ok {
			continue
		}
		clients = append(clients, hosted.NewClient("https://"+env.Handle, access.EnvAPIKey))
	}
	return clients
}

func store() *sessionapi.FileStore {
	root := os.Getenv("TELOS_SESSION_DIR")
	if root == "" {
		root = filepath.Join(".telos", "sessions")
	}
	return sessionapi.NewFileStore(root)
}

func resolveSpecPath(input string) string {
	// Try as-is
	if _, err := os.Stat(input); err == nil {
		abs, _ := filepath.Abs(input)
		return abs
	}
	// Try input/SPEC.md
	candidate := filepath.Join(input, "SPEC.md")
	if _, err := os.Stat(candidate); err == nil {
		abs, _ := filepath.Abs(candidate)
		return abs
	}
	// Try input/spec.md
	candidate = filepath.Join(input, "spec.md")
	if _, err := os.Stat(candidate); err == nil {
		abs, _ := filepath.Abs(candidate)
		return abs
	}
	abs, _ := filepath.Abs(input)
	return abs
}

func getSessionFromAnywhere(sessionID, envID string) (*sessionapi.Session, error) {
	// Try local first
	s := store()
	session, err := s.Get(sessionID)
	if err == nil {
		return session, nil
	}

	// Try hosted
	if envID != "" || config.IsConfigured() {
		for _, client := range hostedSessionClients(envID) {
			session, err := client.GetSession(sessionID)
			if err == nil {
				return session, nil
			}
		}
	}

	return nil, fmt.Errorf("session %s: not found", sessionID)
}

func getTranscriptFromAnywhere(sessionID, envID string) (string, error) {
	s := store()
	text, err := s.Transcript(sessionID)
	if err == nil {
		return text, nil
	}

	if envID != "" || config.IsConfigured() {
		for _, client := range hostedSessionClients(envID) {
			text, err := client.GetTranscript(sessionID)
			if err == nil {
				return text, nil
			}
		}
	}

	return "", fmt.Errorf("session %s transcript: not found", sessionID)
}

func stopSessionAnywhere(sessionID, envID string) (*sessionapi.Session, error) {
	s := store()
	session, err := s.Stop(sessionID)
	if err == nil {
		return session, nil
	}

	if envID != "" || config.IsConfigured() {
		for _, client := range hostedSessionClients(envID) {
			session, err := client.StopSession(sessionID)
			if err == nil {
				return session, nil
			}
		}
	}

	return nil, fmt.Errorf("session %s: not found", sessionID)
}

func skillNames(skills []*spec.Skill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}

func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
