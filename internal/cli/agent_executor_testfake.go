//go:build telos_testfake

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

const testFakeScenarioRelPath = ".telos-testfake/agent.json"

// createAgentExecutor swaps Pi for a deterministic scenario-driven executor
// only in binaries built with -tags telos_testfake. Tagged binaries fail closed
// when the active workspace does not contain the private test fixture file.
func createAgentExecutor(workspace string, cfg LocalRunConfig) (game.AgentExecutor, error) {
	scenarioPath := filepath.Join(workspace, testFakeScenarioRelPath)
	if _, err := os.Stat(scenarioPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("test fake scenario not found: %s", scenarioPath)
		}
		return nil, fmt.Errorf("inspect test fake scenario: %w", err)
	}
	return newTestFakeExecutor(workspace, scenarioPath)
}

type testFakeScenario struct {
	WorkspaceState string         `json:"workspace_state"`
	Turns          []testFakeTurn `json:"turns"`
}

type testFakeTurn struct {
	Role                string            `json:"role"`
	Status              string            `json:"status"`
	Logs                string            `json:"logs"`
	Error               string            `json:"error"`
	Recoverable         bool              `json:"recoverable"`
	CostUSD             float64           `json:"cost_usd"`
	DurationMS          int               `json:"duration_ms"`
	NumTurns            int               `json:"num_turns"`
	InputTokens         int               `json:"input_tokens"`
	OutputTokens        int               `json:"output_tokens"`
	CacheReadTokens     int               `json:"cache_read_tokens"`
	CacheCreationTokens int               `json:"cache_creation_tokens"`
	Model               string            `json:"model"`
	Writes              map[string]string `json:"writes"`
	Deletes             []string          `json:"deletes"`
	SleepMS             int               `json:"sleep_ms"`
}

type testFakeExecutor struct {
	workspace string
	scenario  testFakeScenario
	nextTurn  int
}

func newTestFakeExecutor(workspace string, scenarioPath string) (*testFakeExecutor, error) {
	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("read test fake scenario %s: %w", scenarioPath, err)
	}
	var scenario testFakeScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		return nil, fmt.Errorf("parse test fake scenario %s: %w", scenarioPath, err)
	}
	if len(scenario.Turns) == 0 {
		return nil, fmt.Errorf("test fake scenario %s has no turns", scenarioPath)
	}
	return &testFakeExecutor{workspace: workspace, scenario: scenario}, nil
}

func (e *testFakeExecutor) ExecuteTurn(task string, role string, ts *game.TurnState) game.TurnResult {
	if e.nextTurn >= len(e.scenario.Turns) {
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   "test fake scenario exhausted",
			Error:  "test fake scenario exhausted",
		}
	}
	turn := e.scenario.Turns[e.nextTurn]
	e.nextTurn++

	if turn.Role != "" && turn.Role != role {
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   fmt.Sprintf("test fake role mismatch: got %s, want %s", role, turn.Role),
			Error:  fmt.Sprintf("test fake role mismatch: got %s, want %s", role, turn.Role),
		}
	}
	if turn.SleepMS > 0 {
		deadline := time.Now().Add(time.Duration(turn.SleepMS) * time.Millisecond)
		for time.Now().Before(deadline) {
			if ts != nil && ts.StopRequested != nil && ts.StopRequested() {
				return game.TurnResult{
					Role:        role,
					Status:      game.StatusContinue,
					Logs:        "test fake interrupted by stop request",
					Error:       "local_interrupted:stop_requested",
					Recoverable: true,
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := e.applyWorkspaceChanges(turn); err != nil {
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   err.Error(),
			Error:  err.Error(),
		}
	}

	return game.TurnResult{
		Role:        role,
		Status:      testFakeStatus(turn.Status),
		Logs:        turn.Logs,
		Error:       turn.Error,
		Recoverable: turn.Recoverable,
		Stats: game.TurnStats{
			CostUSD:             turn.CostUSD,
			DurationMS:          turn.DurationMS,
			NumTurns:            turn.NumTurns,
			InputTokens:         turn.InputTokens,
			OutputTokens:        turn.OutputTokens,
			CacheReadTokens:     turn.CacheReadTokens,
			CacheCreationTokens: turn.CacheCreationTokens,
			Model:               turn.Model,
		},
	}
}

func (e *testFakeExecutor) WorkspaceState() string {
	if e.scenario.WorkspaceState != "" {
		return e.scenario.WorkspaceState
	}
	return platform.NewLocalPlatform(e.workspace).WorkspaceState()
}

func (e *testFakeExecutor) CheckpointWorkspace(dest string) bool {
	return platform.NewLocalPlatform(e.workspace).CheckpointWorkspace(dest)
}

func (e *testFakeExecutor) applyWorkspaceChanges(turn testFakeTurn) error {
	for rel, content := range turn.Writes {
		path, err := safeWorkspacePath(e.workspace, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("test fake mkdir %s: %w", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("test fake write %s: %w", rel, err)
		}
	}
	for _, rel := range turn.Deletes {
		path, err := safeWorkspacePath(e.workspace, rel)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("test fake delete %s: %w", rel, err)
		}
	}
	return nil
}

func testFakeStatus(status string) game.AgentStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CONCEDE":
		return game.StatusConcede
	default:
		return game.StatusContinue
	}
}

func safeWorkspacePath(workspace string, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("test fake workspace path is empty")
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("test fake workspace path escapes workspace: %s", rel)
	}
	return filepath.Join(workspace, clean), nil
}
