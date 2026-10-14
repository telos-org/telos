package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestPiProgressPreservesReportAndFinalStatus(t *testing.T) {
	progress := "<progress_update>Orders costing > $25 are rejected without changing your balance.</progress_update>"
	report := "\nChecked restart recovery and duplicate-order handling.\n<status>CONCEDE</status>"
	line := `{"message":{"role":"assistant","content":[{"type":"text","text":"<progress_update>Orders costing > $25 are rejected without changing your balance.</progress_update>"},{"type":"text","text":"\nChecked restart recovery and duplicate-order handling.\n<status>CONCEDE</status>"}]}}`
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, line)
	summary, err := ReadPiSession(path)
	if err != nil || summary.Logs != progress+report || game.ExtractStatus(summary.Logs) != game.StatusConcede {
		t.Fatalf("progress changed the report or status: %+v, %v", summary, err)
	}
	events := piLineEvents(line)
	if len(events) != 1 || events[0].Kind != "progress_update" || events[0].Text != "Orders costing > $25 are rejected without changing your balance." {
		t.Fatalf("progress changed: %#v", events)
	}
}

func TestPiProgressCannotReuseAnEarlierFinalStatus(t *testing.T) {
	for _, tc := range []struct {
		name, message, wantError string
	}{
		{"update-only response", `{"role":"assistant","content":[{"type":"text","text":"<progress_update>Checking the next requirement.</progress_update>"}]}`, ""},
		{"nested status", `{"role":"assistant","content":[{"type":"text","text":"<progress_update>Example:\n<status>CONCEDE</status>\n</progress_update>"}]}`, ""},
		{"truncated update", `{"role":"assistant","stopReason":"length","content":[{"type":"text","text":"<progress_update>Checking recovery."}]}`, "agent_output_truncated:length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pi-session.jsonl")
			writePiSession(t, path, `{"message":{"role":"assistant","content":[{"type":"text","text":"Earlier check passed.\n<status>CONCEDE</status>"}]}}`)
			appendPiSession(t, path, `{"message":`+tc.message+`}`)
			summary, err := ReadPiSession(path)
			if err != nil || summary.Error != tc.wantError || strings.Contains(summary.Logs, "Earlier check passed") || game.ExtractStatus(summary.Logs) != game.StatusContinue {
				t.Fatalf("final response reused earlier success: %+v, %v", summary, err)
			}
		})
	}
}

func TestNewPiExecutorDefaultsToNoTimeout(t *testing.T) {
	exec := NewPiExecutor(nil, "claude-test", "", 0)

	if exec.Timeout != 0 {
		t.Fatalf("timeout should default to disabled, got %d", exec.Timeout)
	}
	if exec.Thinking != "medium" {
		t.Fatalf("thinking: got %q", exec.Thinking)
	}
}

func TestRecoverableAgentFailureRejectsProviderBlockers(t *testing.T) {
	for _, errorText := range []string{
		"403: inactive virtual key",
		"request failed (HTTP 401)",
		"400: Your credit balance is too low to access the Anthropic API",
		"429: insufficient_quota",
	} {
		if recoverableAgentFailure(errorText) {
			t.Fatalf("provider blocker marked recoverable: %q", errorText)
		}
	}
	for _, errorText := range []string{
		"429: provider at capacity",
		"502: no healthy upstream",
	} {
		if !recoverableAgentFailure(errorText) {
			t.Fatalf("transient error marked non-recoverable: %q", errorText)
		}
	}
}

func TestBuildPiArgvUsesTextModeWithoutSessionByDefault(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "", "")
	if len(argv) != 6 {
		t.Fatalf("expected 6 args, got %d", len(argv))
	}
	if argv[0] != "sh" {
		t.Errorf("first arg: got %q", argv[0])
	}
	if argv[4] != "claude-test" {
		t.Errorf("model arg: got %q", argv[4])
	}
	if argv[5] != "high" {
		t.Errorf("thinking arg: got %q", argv[5])
	}
	if !strings.Contains(argv[2], `prompt="${TELOS_TASK}"`) {
		t.Errorf("task prompt is not expanded from env: %s", argv[2])
	}
	if !strings.Contains(argv[2], `--mode text`) {
		t.Errorf("pi should run in text mode: %s", argv[2])
	}
	if !strings.Contains(argv[2], `--no-session`) {
		t.Errorf("fallback path should stay ephemeral: %s", argv[2])
	}
	if strings.Contains(argv[2], `--mode json`) {
		t.Errorf("pi should not use the streaming json event mode: %s", argv[2])
	}
	if strings.Contains(argv[2], `--no-extensions`) {
		t.Errorf("pi should allow configured extensions: %s", argv[2])
	}
	if !strings.Contains(argv[2], `TELOS_PI_APPEND_SYSTEM_PROMPT`) ||
		!strings.Contains(argv[2], `--append-system-prompt "$append_file"`) {
		t.Errorf("hosted append system prompt is not wired: %s", argv[2])
	}
}

func TestBuildPiArgvUsesTaskFileAndSessionFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "/tmp/task.md", "/tmp/pi-session.jsonl")
	if len(argv) != 8 {
		t.Fatalf("expected 8 args, got %d", len(argv))
	}
	if argv[6] != "@/tmp/task.md" {
		t.Errorf("task file arg: got %q", argv[6])
	}
	if argv[7] != "/tmp/pi-session.jsonl" {
		t.Errorf("session file arg: got %q", argv[7])
	}
	if !strings.Contains(argv[2], `--session "$4"`) {
		t.Errorf("pi session file is not selected from argv: %s", argv[2])
	}
	if strings.Contains(argv[2], `-p "${TELOS_TASK}"`) {
		t.Errorf("task env is still expanded directly into argv: %s", argv[2])
	}
}

func TestBuildPiArgvUsesSessionFileWithoutTaskFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "", "/tmp/pi-session.jsonl")
	if len(argv) != 8 {
		t.Fatalf("expected 8 args with empty task placeholder, got %d", len(argv))
	}
	if argv[6] != "" {
		t.Errorf("task placeholder: got %q", argv[6])
	}
	if argv[7] != "/tmp/pi-session.jsonl" {
		t.Errorf("session file arg: got %q", argv[7])
	}
}

func TestExecuteTurnIncludesStderrOnPiFailure(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(bin, "pi")
	script := "#!/bin/sh\necho \"EROFS: read-only file system\" >&2\nexit 1\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home}
	exec := NewPiExecutor(p, "test-model", "high", 0)

	result := exec.ExecuteTurn("do it", "prover", nil)

	if result.Error == "" || !strings.Contains(result.Error, "pi_failed:1") {
		t.Fatalf("error: got %q", result.Error)
	}
	if !strings.Contains(result.Logs, "[stderr]") ||
		!strings.Contains(result.Logs, "EROFS: read-only file system") {
		t.Fatalf("logs should include stderr, got %q", result.Logs)
	}
}

func TestExecuteTurnUsesEnvPromptForNormalSessionTask(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(bin, "pi")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$HOME/argv.txt"
printf '%s' "$TELOS_TASK" > "$HOME/task-env.txt"
session=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--session" ]; then
    shift
    session="$1"
  fi
  shift || true
done
mkdir -p "$(dirname "$session")"
printf '%s\n' '{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"done\n<status>CONCEDE</status>\n"}],"stopReason":"stop"}}' > "$session"
`
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	state := game.NewPVGState("spec", t.TempDir(), "sess")
	turn := state.Turn(1, 1, "prover")
	task := "small controller task"
	if err := game.WriteTurnTask(turn, task); err != nil {
		t.Fatal(err)
	}

	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home}
	exec := NewPiExecutor(p, "test-model", "high", 0)

	result := exec.ExecuteTurn(task, "prover", turn)

	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %q logs=%q error=%q", result.Status, result.Logs, result.Error)
	}
	taskEnv, err := os.ReadFile(filepath.Join(home, "task-env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(taskEnv) != task {
		t.Fatalf("TELOS_TASK: got %q want %q", string(taskEnv), task)
	}
	argv, err := os.ReadFile(filepath.Join(home, "argv.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), "@"+turn.TaskPath()) {
		t.Fatalf("normal session task should not use Pi @file prompt:\n%s", string(argv))
	}
}

func TestExecuteTurnTreatsTimeoutAsTerminalFailure(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(bin, "pi")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home}
	exec := NewPiExecutor(p, "test-model", "high", 1)

	result := exec.ExecuteTurn("do it", "prover", nil)

	if result.Error != "local_timeout:1" {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.Recoverable {
		t.Fatalf("timeout should be terminal, got recoverable result: %#v", result)
	}
}

func TestExecuteTurnResumesWithoutReplayingExistingActivity(t *testing.T) {
	old := `{"message":{"role":"assistant","content":[{"type":"text","text":"<progress_update>old</progress_update>\n<status>CONCEDE</status>"},{"type":"toolCall","name":"bash","arguments":{"command":"old command"}}],"usage":{"input":10,"output":20,"cost":{"total":0.25}}}}` + "\n"
	newLine := `{"message":{"role":"assistant","content":[{"type":"text","text":"<progress_update>new</progress_update>\n<status>CONTINUE</status>"},{"type":"toolCall","name":"bash","arguments":{"command":"new command"}}],"usage":{"input":3,"output":4,"cost":{"total":0.5}}}}` + "\n"
	exec, turn := newPiResumeTestExecutor(t, old, newLine)
	var events []game.LiveAgentEvent
	turn.OnLiveEvent = func(event game.LiveAgentEvent) { events = append(events, event) }
	result := exec.ExecuteTurn("resume verifier", "verifier", turn)
	if result.Error != "" || result.Status != game.StatusContinue || strings.Contains(result.Logs, "old") {
		t.Fatalf("resumed result = %+v", result)
	}
	want := []game.LiveAgentEvent{
		{Kind: "progress_update", Text: "new"},
		{Kind: "tool", Text: "Running shell command"},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("resumed activity = %#v, want %#v", events, want)
	}
	// Nothing accounted for the interrupted attempt's usage yet. Both pieces
	// of this one turn are reported together when it finally completes.
	if result.Stats.InputTokens != 13 || result.Stats.OutputTokens != 24 || result.Stats.CostUSD != 0.75 {
		t.Fatalf("interrupted turn usage was lost: %+v", result.Stats)
	}
}

func TestExecuteTurnRequiresNewAssistantOutputAfterResume(t *testing.T) {
	old := `{"message":{"role":"assistant","content":[{"type":"text","text":"<status>CONCEDE</status>"}],"usage":{"input":10,"cost":{"total":0.25}}}}`
	for _, tc := range []struct {
		name, trailingNewline, appended string
	}{
		{"no new bytes", "\n", ""},
		{"old record gets its newline", "", "\n"},
		{"new user message only", "\n", `{"message":{"role":"user","content":"resume"}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec, turn := newPiResumeTestExecutor(t, old+tc.trailingNewline, tc.appended)
			var events []game.LiveAgentEvent
			turn.OnLiveEvent = func(event game.LiveAgentEvent) { events = append(events, event) }
			result := exec.ExecuteTurn("resume verifier", "verifier", turn)
			if result.Status != game.StatusContinue || !result.Recoverable || !strings.Contains(result.Error, "no new assistant message") {
				t.Fatalf("reused stale concession: %+v", result)
			}
			if len(events) != 0 || result.Stats.InputTokens != 10 || result.Stats.CostUSD != 0.25 {
				t.Fatalf("replayed activity or lost prior usage: events=%#v stats=%+v", events, result.Stats)
			}
		})
	}
}

func TestExecuteTurnCompletesPartialRecordAfterResume(t *testing.T) {
	line := piProgressLine(strings.Repeat("x", 96*1024) + " café")
	split := strings.Index(line, "é") + 1
	exec, turn := newPiResumeTestExecutor(t, piProgressLine("old")+"\n"+line[:split], line[split:]+"\n")
	var events []game.LiveAgentEvent
	turn.OnLiveEvent = func(event game.LiveAgentEvent) { events = append(events, event) }
	result := exec.ExecuteTurn("resume verifier", "verifier", turn)
	if result.Error != "" || len(events) != 1 || events[0].Text != strings.Repeat("x", 96*1024)+" café" {
		t.Fatalf("resumed partial record lost: error=%q events=%d", result.Error, len(events))
	}
}

func TestPiResumeRejectsChangedHistory(t *testing.T) {
	for _, change := range []string{"truncation", "replacement", "same-inode regrowth"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pi-session.jsonl")
			initial := piTestSessionHeader + "\n" + piProgressLine(strings.Repeat("old", 100)) + "\n" + piProgressLine("partial")[:25]
			if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
				t.Fatal(err)
			}
			baseline, err := capturePiSessionBaseline(path)
			if err != nil {
				t.Fatal(err)
			}
			var events []game.LiveAgentEvent
			projector := startPiLiveProjector(path, &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) {
				events = append(events, event)
			}}, baseline)
			updatedPath, updated := path, piTestSessionHeader+"\n"+piProgressLine("new")+"\n"
			if change == "replacement" {
				updatedPath += ".new"
			}
			if change != "truncation" {
				updated += strings.Repeat("\n", len(initial))
			}
			if err := os.WriteFile(updatedPath, []byte(updated), 0o644); err != nil {
				t.Fatal(err)
			}
			if change == "replacement" {
				if err := os.Rename(updatedPath, path); err != nil {
					t.Fatal(err)
				}
			}
			projector.Stop()
			_, err = readPiSession(path, baseline)
			if !errors.Is(err, errPiSessionChanged) || !errors.Is(projector.integrityErr, errPiSessionChanged) || len(events) != 0 {
				t.Fatalf("changed history was treated as valid output: err=%v events=%#v", err, events)
			}
		})
	}
}

func TestExecuteTurnRejectsUnsupportedSessionBeforeStartingPi(t *testing.T) {
	for _, header := range []string{
		`{"type":"session","version":2,"id":"test"}`, `{"type":"session","version":`, `{"type":"session","version":4,"id":"test"}`,
		`{"type":"session","version":3}`, `{"type":"session","version":3,"id":42}`,
	} {
		t.Run(header, func(t *testing.T) {
			executor, turn := newPiResumeTestExecutor(t, "", piProgressLine("new")+"\n")
			initial := header + "\n" + piProgressLine("old") + "\n"
			if err := os.WriteFile(turn.PiSessionPath(), []byte(initial), 0o644); err != nil {
				t.Fatal(err)
			}
			result := executor.ExecuteTurn("resume", "verifier", turn)
			data, err := os.ReadFile(turn.PiSessionPath())
			if err != nil || string(data) != initial || result.StateError == "" || result.PreparationError != nil || result.Recoverable || result.Status == game.StatusConcede {
				t.Fatalf("unsafe session was launched: result=%+v readErr=%v", result, err)
			}
		})
	}
}

func TestExecuteTurnRetriesPreparationFailureWithoutLaunchingPi(t *testing.T) {
	for _, failure := range []string{"unreadable session", "missing workspace"} {
		t.Run(failure, func(t *testing.T) {
			executor, turn := newPiResumeTestExecutor(t, "", piProgressLine("repaired")+"\n")
			path := turn.PiSessionPath()
			if failure == "unreadable session" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				executor.Platform.Workspace = filepath.Join(t.TempDir(), "missing-workspace")
			}
			result := executor.ExecuteTurn("resume", "verifier", turn)
			if result.PreparationError == nil || result.StateError != "" || result.Error != "" {
				t.Fatalf("preparation failure was not retryable: %+v", result)
			}
			startedPath := filepath.Join(executor.Platform.Env["HOME"], "pi-started")
			if _, err := os.Stat(startedPath); !os.IsNotExist(err) {
				t.Fatalf("Pi started before repair: %v", err)
			}
			if failure == "unreadable session" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				writePiSession(t, path, piTestSessionHeader)
			} else if err := os.Mkdir(executor.Platform.Workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			result = executor.ExecuteTurn("resume", "verifier", turn)
			if result.PreparationError != nil || result.StateError != "" || result.Error != "" || !strings.Contains(result.Logs, "repaired") {
				t.Fatalf("same turn could not run after repair: %+v", result)
			}
			if data, err := os.ReadFile(startedPath); err != nil || string(data) != "started\n" {
				t.Fatalf("expected exactly one Pi launch: %q, %v", data, err)
			}
		})
	}
}

func TestExecuteTurnHonorsStopBeforeStartingPi(t *testing.T) {
	executor, turn := newPiResumeTestExecutor(t, "", piProgressLine("must not run")+"\n")
	turn.StopRequested = func() bool { return true }
	result := executor.ExecuteTurn("resume", "verifier", turn)
	if result.PreparationError == nil || !strings.Contains(result.PreparationError.Error(), "stop_requested") || result.StateError != "" || result.Error != "" {
		t.Fatalf("pre-start stop consumed an attempt: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(executor.Platform.Env["HOME"], "pi-started")); !os.IsNotExist(err) {
		t.Fatalf("Pi started despite stop request: %v", err)
	}
}

func TestExecuteTurnCanRecoverFromMissingCredentialsBeforeSessionCreation(t *testing.T) {
	newLine := `{"message":{"role":"assistant","content":[{"type":"text","text":"credentials repaired"}],"usage":{"input":3,"cost":{"total":0.5}}}}`
	executor, turn := newPiResumeTestExecutor(t, "", piTestSessionHeader+"\n"+newLine+"\n")
	if err := os.Remove(turn.PiSessionPath()); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(executor.Platform.Env["HOME"], ".local", "bin", "pi")
	script, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	// Pi validates credentials before it writes the first session record.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'No API key found for anthropic.' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := executor.ExecuteTurn("run", "prover", turn)
	blocker, _ := game.AgentFailureBlocker(result.Error)
	if result.StateError != "" || result.PreparationError != nil || blocker != "agent_credentials_missing" || result.Stats.CostUSD != 0 {
		t.Fatalf("missing credentials became unknown usage: %+v", result)
	}
	if err := os.WriteFile(binary, script, 0o755); err != nil {
		t.Fatal(err)
	}
	result = executor.ExecuteTurn("run", "prover", turn)
	if result.StateError != "" || result.Error != "" || result.Logs != "credentials repaired" || result.Stats.CostUSD != 0.5 {
		t.Fatalf("credential repair could not recover: %+v", result)
	}
}

func TestExecuteTurnBlocksWhenNewSessionIsUnreadableAfterLaunch(t *testing.T) {
	for _, action := range []string{`rm "$session"`, `rm "$session"; mkdir "$session"`} {
		t.Run(action, func(t *testing.T) {
			executor, turn := newPiResumeTestExecutor(t, "", piProgressLine("new")+"\n")
			if err := os.Remove(turn.PiSessionPath()); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(executor.Platform.Env["HOME"], ".local", "bin", "pi")
			script, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binary, append(script, []byte(action+"\n")...), 0o755); err != nil {
				t.Fatal(err)
			}
			result := executor.ExecuteTurn("run", "verifier", turn)
			if result.StateError == "" || !strings.Contains(result.StateError, "usage is unknown") || result.PreparationError != nil || result.Recoverable || result.Status == game.StatusConcede {
				t.Fatalf("unverifiable post-launch accounting was accepted: %+v", result)
			}
		})
	}
}

func TestExecuteTurnPreservesKnownUsageOnProcessFailure(t *testing.T) {
	old := `{"message":{"role":"assistant","content":[{"type":"text","text":"<status>CONCEDE</status>"}],"usage":{"input":10,"cost":{"total":0.25}}}}` + "\n"
	executor, turn := newPiResumeTestExecutor(t, old, "")
	binary := filepath.Join(executor.Platform.Env["HOME"], ".local", "bin", "pi")
	script, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, append(script, []byte("exit 1\n")...), 0o755); err != nil {
		t.Fatal(err)
	}
	result := executor.ExecuteTurn("resume", "verifier", turn)
	if result.StateError != "" || result.PreparationError != nil || !result.Recoverable || result.Error != "pi_failed:1" || result.Status != game.StatusContinue || result.Stats.InputTokens != 10 || result.Stats.CostUSD != 0.25 {
		t.Fatalf("process failure lost known usage or reused stale verdict: %+v", result)
	}
}

func TestPiResumeRejectsRewritingNewlyObservedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	initial := piTestSessionHeader + "\n" + piProgressLine("old") + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline, err := capturePiSessionBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []game.LiveAgentEvent
	projector := &piLiveProjector{
		sessionPath: path, offset: baseline.offset, fileInfo: baseline.fileInfo, baseline: baseline,
		turnState: &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) { events = append(events, event) }},
	}
	appendPiSession(t, path, piProgressLine("new"))
	projector.observeSessionFile(false)
	// Keep the original resume boundary intact, but overwrite output already
	// observed during this attempt and regrow beyond the current cursor.
	if err := os.WriteFile(path, []byte(initial+piProgressLine("replacement")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projector.observeSessionFile(true)
	if !errors.Is(projector.integrityErr, errPiSessionChanged) || !slices.Equal(events, []game.LiveAgentEvent{{Kind: "progress_update", Text: "new"}}) {
		t.Fatalf("replayed or lost newly observed history: err=%v events=%#v", projector.integrityErr, events)
	}
}

func TestExecuteTurnDoesNotConcedeFromMalformedOldTail(t *testing.T) {
	old := `{"message":{"role":"assistant","content":[{"type":"text","text":"<status>CONCEDE</status>"}]}}!`
	newAssistant := `{"message":{"role":"assistant","content":[{"type":"text","text":"<status>CONCEDE</status>"}],"usage":{"cost":{"total":0.5}}}}`
	for _, appended := range []string{"", "\n", "\n" + newAssistant + "\n"} {
		executor, turn := newPiResumeTestExecutor(t, old, appended)
		result := executor.ExecuteTurn("resume", "verifier", turn)
		if result.Status == game.StatusConcede || result.Recoverable || !strings.Contains(result.StateError, "invalid Pi session record") || result.Stats.CostUSD != 0 {
			t.Fatalf("malformed historical tail accepted with untrustworthy accounting: %+v", result)
		}
	}
}

func TestExecuteTurnBlocksWhenHistoryChangesAndUsageIsUnknown(t *testing.T) {
	old := `{"message":{"role":"assistant","content":[{"type":"text","text":"old"}],"usage":{"input":10,"cost":{"total":0.25}}}}`
	newLine := `{"message":{"role":"assistant","content":[{"type":"text","text":"<status>CONCEDE</status>"}],"usage":{"input":3,"cost":{"total":0.5}}}}`
	executor, turn := newPiResumeTestExecutor(t, old+"\n", piTestSessionHeader+"\n"+newLine+"\n")
	binary := filepath.Join(executor.Platform.Env["HOME"], ".local", "bin", "pi")
	script, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte(strings.ReplaceAll(string(script), `>> "$session"`, `> "$session"`)), 0o755); err != nil {
		t.Fatal(err)
	}
	result := executor.ExecuteTurn("resume", "verifier", turn)
	if result.StateError == "" || !strings.Contains(result.StateError, "usage is unknown") || result.Recoverable || result.Status == game.StatusConcede || result.Stats.CostUSD != 0 {
		t.Fatalf("lost history accepted with incomplete spending: %+v", result)
	}
}

const piTestSessionHeader = `{"type":"session","version":3,"id":"test-session"}`

func newPiResumeTestExecutor(t *testing.T, initial, appended string) (*PiExecutor, *game.TurnState) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf 'started\n' >> "$HOME/pi-started"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--session" ]; then
    shift
    session="$1"
  fi
  shift || true
done
printf '%s' "$TELOS_TEST_PI_APPEND" >> "$session"
`
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	turn := game.NewPVGState("spec", t.TempDir(), "sess").Turn(1, 2, "verifier")
	if err := game.WriteTurnTask(turn, "resume verifier"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(turn.PiSessionPath(), []byte(piTestSessionHeader+"\n"+initial), 0o644); err != nil {
		t.Fatal(err)
	}
	p := platform.NewLocalPlatform(t.TempDir())
	p.Env = map[string]string{"HOME": home, "TELOS_TEST_PI_APPEND": appended}
	return NewPiExecutor(p, "test-model", "high", 0), turn
}

func TestReadPiSessionExtractsAssistantTextStatsAndTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"u","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"user","content":"do it","timestamp":1770000000000}}`)
	appendPiSession(t, path, `{"type":"message","id":"t","parentId":"u","timestamp":"2026-05-21T00:00:02Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"ok"}],"isError":false,"timestamp":1770000000001}}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":"t","timestamp":"2026-05-21T00:00:03Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.5","stopReason":"stop","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Implemented it.\n\n<status>CONCEDE</status>\n"}],"usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":40,"totalTokens":100,"cost":{"input":0.1,"output":0.2,"cacheRead":0.3,"cacheWrite":0.4,"total":1.0}},"timestamp":1770000000002}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Logs != "Implemented it.\n\n<status>CONCEDE</status>\n" {
		t.Fatalf("logs: got %q", summary.Logs)
	}
	if summary.Error != "" {
		t.Fatalf("error: got %q", summary.Error)
	}
	want := game.TurnStats{
		CostUSD:             1.0,
		NumTurns:            1,
		InputTokens:         10,
		OutputTokens:        20,
		CacheReadTokens:     30,
		CacheCreationTokens: 40,
		Model:               "gpt-5.5",
	}
	if summary.Stats != want {
		t.Fatalf("stats: got %+v want %+v", summary.Stats, want)
	}
}

func TestReadPiSessionUsesLastAssistantTextAndAggregatesAssistantUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"first"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}}}`)
	appendPiSession(t, path, `{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-05-21T00:00:02Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"second"}],"usage":{"input":2,"output":3,"cacheRead":4,"cacheWrite":5,"cost":{"total":0.6}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Logs != "second" {
		t.Fatalf("logs: got %q", summary.Logs)
	}
	if summary.Stats.InputTokens != 3 || summary.Stats.OutputTokens != 4 ||
		summary.Stats.CacheReadTokens != 4 || summary.Stats.CacheCreationTokens != 5 ||
		summary.Stats.CostUSD != 0.7 {
		t.Fatalf("stats should sum all assistant usage: %+v", summary.Stats)
	}
}

func TestReadPiSessionMapsLengthStopToRecoverableError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"length","content":[{"type":"text","text":"partial"}],"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.3}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Error != "agent_output_truncated:length" {
		t.Fatalf("error: got %q", summary.Error)
	}
}

func TestReadPiSessionIgnoresTransientErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"error","errorMessage":"overloaded_error: try again","content":[{"type":"text","text":"partial"}],"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.3}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Error != "" {
		t.Fatalf("transient error should be ignored, got %q", summary.Error)
	}
}

func TestReadPiSessionRequiresAssistantMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)

	_, err := ReadPiSession(path)
	if err == nil || !strings.Contains(err.Error(), "no assistant message") {
		t.Fatalf("expected no assistant error, got %v", err)
	}
}

func TestPiLineEventsProjectsSafeToolCallProgress(t *testing.T) {
	events := piLineEvents(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"read","arguments":{"path":"/tmp/session/spec.md"}},{"type":"toolCall","name":"read","arguments":{"path":"/tmp/other/spec.md"}},{"type":"toolCall","name":"bash","arguments":{"command":"kubectl get pods --token SECRET"}},{"type":"toolCall","name":"bash","arguments":{"command":"git status --short"}}]}}`)

	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Kind+":"+event.Text)
	}
	want := []string{
		"tool:Reading session/spec.md",
		"tool:Reading other/spec.md",
		"tool:Running kubectl",
		"tool:Updating workspace",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\ngot\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(strings.Join(got, "\n"), "SECRET") {
		t.Fatalf("tool progress leaked command contents: %v", got)
	}
}

func TestPiLiveProjectorPreservesRepeatedActivityWithoutReplayingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	line := `{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"read","arguments":{"path":"/workspace/config.go"}}]}}`
	var events []game.LiveAgentEvent
	projector := &piLiveProjector{
		sessionPath: path,
		turnState: &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) {
			events = append(events, event)
		}},
	}

	writePiSession(t, path, line)
	projector.observeSessionFile(false)
	projector.observeSessionFile(false)
	appendPiSession(t, path, line)
	projector.observeSessionFile(false)

	if len(events) != 2 {
		t.Fatalf("repeated activity count = %d, want 2: %#v", len(events), events)
	}
	for _, event := range events {
		if event.Kind != "tool" || event.Text != "Reading workspace/config.go" {
			t.Fatalf("unexpected event: %#v", event)
		}
	}
}

func TestPiLiveProjectorBuffersPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	var events []game.LiveAgentEvent
	projector := &piLiveProjector{
		sessionPath: path,
		turnState: &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) {
			events = append(events, event)
		}},
	}
	// The file may not exist when the watcher starts.
	projector.observeSessionFile(false)
	writePiSession(t, path, `{"message":{"role":"user","content":"ignored"}}`)
	projector.observeSessionFile(false)

	// A record can exceed bufio.Scanner's default limit and split inside UTF-8.
	body := strings.Repeat("x", 96*1024) + " café"
	line := piProgressLine(body)
	split := strings.Index(line, "é") + 1
	for _, part := range []string{line[:split], line[split:]} {
		appendPiBytes(t, path, part)
		projector.observeSessionFile(false)
		projector.observeSessionFile(false)
		if len(events) != 0 {
			t.Fatalf("emitted an unfinished record: %#v", events)
		}
	}
	appendPiBytes(t, path, "\ninvalid JSON\n\n"+piProgressLine("next")+"\n")
	projector.observeSessionFile(false)
	projector.observeSessionFile(true)
	want := []game.LiveAgentEvent{
		{Kind: "progress_update", Text: body},
		{Kind: "progress_update", Text: "next"},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("partial records were lost, changed, or replayed (got %d events)", len(events))
	}
}

func TestPiLiveProjectorFlushesFinalLineOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	var events []game.LiveAgentEvent
	projector := &piLiveProjector{
		sessionPath: path,
		turnState: &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) {
			events = append(events, event)
		}},
	}
	if err := os.WriteFile(path, []byte(piProgressLine("last")), 0o644); err != nil {
		t.Fatal(err)
	}
	projector.observeSessionFile(false)
	if len(events) != 0 {
		t.Fatal("emitted final record before the final drain")
	}
	projector.observeSessionFile(true)
	projector.observeSessionFile(true)
	appendPiBytes(t, path, "\n")
	projector.observeSessionFile(false)
	if !slices.Equal(events, []game.LiveAgentEvent{{Kind: "progress_update", Text: "last"}}) {
		t.Fatalf("final drain events = %#v", events)
	}
}

func TestPiLiveProjectorResetsOnFileChange(t *testing.T) {
	for _, change := range []string{"same-size replacement", "larger replacement", "truncation"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pi-session.jsonl")
			var events []game.LiveAgentEvent
			projector := &piLiveProjector{
				sessionPath: path,
				turnState: &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) {
					events = append(events, event)
				}},
			}
			original := piProgressLine("old") + "\n" + piProgressLine("unfinished")[:25]
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			projector.observeSessionFile(false)
			replacement := piProgressLine("new") + "\n"
			if change == "truncation" {
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
				projector.observeSessionFile(false)
				appendPiBytes(t, path, replacement)
			} else {
				replacement += strings.Repeat("\n", len(original)-len(replacement))
				if change == "larger replacement" {
					replacement += "\n"
				}
				if err := os.WriteFile(path+".new", []byte(replacement), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".new", path); err != nil {
					t.Fatal(err)
				}
			}
			projector.observeSessionFile(false)
			projector.observeSessionFile(true)
			want := []game.LiveAgentEvent{
				{Kind: "progress_update", Text: "old"},
				{Kind: "progress_update", Text: "new"},
			}
			if !slices.Equal(events, want) {
				t.Fatalf("events after file change = %#v, want %#v", events, want)
			}
		})
	}
}

func TestPiLiveProjectorDefersConcurrentAppendUntilNextPoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, piProgressLine("first"))
	var events []game.LiveAgentEvent
	projector := &piLiveProjector{
		sessionPath: path,
		turnState: &game.TurnState{OnLiveEvent: func(event game.LiveAgentEvent) {
			events = append(events, event)
			if len(events) == 1 {
				appendPiSession(t, path, piProgressLine("second"))
			}
		}},
	}
	projector.observeSessionFile(false)
	if len(events) != 1 {
		t.Fatalf("poll should stop at its initial file size, got %d events", len(events))
	}
	projector.observeSessionFile(false)
	projector.observeSessionFile(true)
	want := []game.LiveAgentEvent{
		{Kind: "progress_update", Text: "first"},
		{Kind: "progress_update", Text: "second"},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

// Run this same benchmark against both revisions. Setup and appends are outside
// the timer; allocations and time measure one live poll after history was read.
func BenchmarkPiLiveProjectorPoll(b *testing.B) {
	for _, historyMiB := range []int{1, 16, 64} {
		for _, appendEvent := range []bool{false, true} {
			name := fmt.Sprintf("history_%dMiB/append_%t", historyMiB, appendEvent)
			b.Run(name, func(b *testing.B) {
				path := filepath.Join(b.TempDir(), "pi-session.jsonl")
				f, err := os.Create(path)
				if err != nil {
					b.Fatal(err)
				}
				defer f.Close()
				chunk := strings.Repeat("{\"type\":\"session\"}\n", 4096)
				var size int64
				for size < int64(historyMiB*1024*1024) {
					n, err := f.WriteString(chunk)
					if err != nil {
						b.Fatal(err)
					}
					size += int64(n)
				}
				count := 0
				projector := &piLiveProjector{
					sessionPath: path,
					offset:      size,
					turnState: &game.TurnState{OnLiveEvent: func(game.LiveAgentEvent) {
						count++
					}},
				}
				line := piProgressLine("benchmark") + "\n"
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if appendEvent {
						b.StopTimer()
						if _, err := f.WriteString(line); err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
					}
					projector.observeSessionFile(false)
				}
				b.StopTimer()
				if appendEvent && count != b.N {
					b.Fatalf("emitted %d events, want %d", count, b.N)
				}
			})
		}
	}
}

func piProgressLine(text string) string {
	return fmt.Sprintf(`{"message":{"role":"assistant","content":[{"type":"text","text":"<progress_update>%s</progress_update>"}]}}`, text)
}

func writePiSession(t *testing.T, path string, line string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendPiSession(t *testing.T, path string, line string) {
	t.Helper()
	appendPiBytes(t, path, line+"\n")
}

func appendPiBytes(t *testing.T, path string, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}
