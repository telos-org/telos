//go:build unix

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/runtimeclient"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionupdate"
	"github.com/telos-org/telos/internal/sessionworker"
)

func store() *sessionapi.FileStore {
	root, err := cli.DefaultLocalSessionRoot("")
	if err != nil {
		root = os.Getenv("TELOS_SESSION_DIR")
		if root == "" {
			root = filepath.Join(".telos", "sessions")
		}
	}
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	store.OnSpecUpdate = sessionupdate.ProjectSpecUpdate
	return store
}

func stopSessionAnywhere(sessionID string) (*sessionapi.Session, error) {
	s := store()
	var sessionDir string
	if session, err := s.Get(sessionID); err == nil && session.SessionDir != nil {
		sessionDir = *session.SessionDir
	}
	session, err := s.Stop(sessionID)
	if err == nil {
		if sessionDir != "" {
			if err := sessionworker.Stop(sessionDir); err != nil {
				return nil, err
			}
		}
		return session, nil
	}

	return nil, localSessionNotFoundError(sessionID)
}

func localSessionRoot() string {
	root, err := cli.DefaultLocalSessionRoot("")
	if err == nil {
		return root
	}
	return filepath.Join(".telos", "sessions")
}

func listLocalSessions() []sessionapi.Session {
	sessions, err := store().List()
	if err != nil {
		return nil
	}
	return sessions
}

func resolveLocalRunConfigFromFlags(
	fs *flag.FlagSet,
	workspace string,
	model string,
	thinking string,
	maxCostUSD float64,
) (cli.LocalRunConfig, error) {
	cost, err := positiveFloatOption(fs, "max-cost-usd", maxCostUSD, "TELOS_MAX_COST_USD", 20.0)
	if err != nil {
		return cli.LocalRunConfig{}, err
	}
	level, err := thinkingOption(fs, thinking)
	if err != nil {
		return cli.LocalRunConfig{}, err
	}
	if level == "" {
		level = cli.DefaultLocalThinking
	}
	return cli.LocalRunConfig{
		Workspace:  stringOption(fs, "workspace", workspace, "TELOS_WORKSPACE"),
		Model:      modelOption(fs, model),
		Thinking:   level,
		MaxCostUSD: &cost,
	}, nil
}

func printLocalLaunch(out io.Writer, action string, session *cli.LocalSession) {
	workspace := shellQuote(session.WorkspaceScope)
	fmt.Fprintf(out, "%s %s\n\n", action, session.SpecName)
	printSummaryField(out, "Name", session.SpecName)
	printSummaryField(out, "Target", "local")
	printSummaryField(out, "Status", "active")
	printSummaryField(out, "Cost", "-")
	printSummaryField(out, "Goal", session.SessionID)
	printSummaryField(out, "Workspace", session.WorkspaceScope)
	fmt.Fprintln(out)
	printSummaryField(out, "Describe", fmt.Sprintf("cd %s && telos describe %s", workspace, session.SessionID))
	printSummaryField(out, "Logs", fmt.Sprintf("cd %s && telos logs %s", workspace, session.SessionID))
}

func runCloudChildSession(
	specArg string,
	ctx rootContext,
	until untilConfig,
	runtimeConfig sessionRuntimeConfig,
	jsonOut bool,
) {
	req, err := sessionCreateRequestForSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	req.ParentSessionID = &ctx.sessionID
	if until.ReviewCycles > 0 {
		req.Until = &until.ReviewCycles
	}
	if until.Seconds > 0 {
		req.UntilSeconds = &until.Seconds
	}
	applySessionRuntimeConfig(&req, runtimeConfig)
	session, err := runtimeclient.New(ctx.endpoint, ctx.token).CreateSession(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{"goal": goalJSON(session)})
		return
	}
	printSessionReceipt(os.Stdout, "submitted", session)
}

func localRunAvailable() bool {
	_, hosted := rootSessionContext()
	_, err := sessionworker.ResolveTelosd()
	return hosted || err == nil
}

func getLocalSession(id string) (*sessionapi.Session, error)      { return store().Get(id) }
func getLocalTranscript(id string) (string, error)                { return store().Transcript(id) }
func getLocalEvents(id string) ([]sessionapi.SessionEvent, error) { return store().Events(id) }

func cmdRun(args []string) {
	fs := newCommandFlagSet("run", "telos run SPEC.md [flags]")
	workspace := fs.String("workspace", "", "Workspace directory for local specs")
	model := fs.String("model", "", "Model as <provider>/<model> (e.g. openai-codex/gpt-5.5); defaults to $TELOS_MODEL")
	thinking := fs.String("thinking", "", "Thinking effort: low, medium, high, or xhigh; defaults to $TELOS_THINKING, then high for local runs")
	untilValue := fs.String("until", "", "Run at most N review cycles or duration like 30m")
	maxCostUSD := fs.Float64("max-cost-usd", 0, "Maximum local execution cost in USD; defaults to 20")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	until, err := untilFlagValue(fs, *untilValue)
	if err != nil {
		exitWithError(err)
	}
	requireArgCount(fs, 1, "one SPEC.md")
	specArg := fs.Arg(0)
	specPath, hasLocalSpec := existingSpecPath(specArg)
	if ctx, ok := rootSessionContext(); ok {
		if flagNamesSet(fs, "workspace") {
			exitWithError(errors.New("local run config flags are not supported inside a Telos Goal"))
		}
		cfg, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD)
		if err != nil {
			exitWithError(err)
		}
		if !flagNameSet(fs, "model") {
			cfg.Model = ""
		}
		if !flagNameSet(fs, "thinking") {
			cfg.Thinking = ""
		}
		runCloudChildSession(specArg, ctx, until, cfg, *jsonOut)
		return
	}
	if !hasLocalSpec {
		exitWithError(specFileNotFound(specArg))
	}
	if err := prepareRegistrySkills(specPath); err != nil {
		exitWithError(err)
	}
	cfg, err := resolveLocalRunConfigFromFlags(fs, *workspace, *model, *thinking, *maxCostUSD)
	if err != nil {
		exitWithError(err)
	}
	cfg.Until, cfg.UntilSeconds = until.ReviewCycles, until.Seconds
	session, err := cli.SubmitLocalSession(specPath, cfg)
	if err != nil {
		exitWithError(err)
	}
	if *jsonOut {
		printJSON(map[string]interface{}{
			"goal_id":          session.SessionID,
			"goal_dir":         session.SessionDir,
			"workspace":        session.WorkspaceScope,
			"active_workspace": session.ActiveWorkspace,
			"spec_name":        session.SpecName,
			"status":           "running",
		})
	} else {
		printLocalLaunch(os.Stdout, "submitted", session)
	}
}
