package game

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// runCheckpoint is the execution authority, separate from best-effort activity
// logs. Result counts completed turns; the next turn is always Rounds + 1.
// Workers must hold the session ownership lock throughout Run.
type runCheckpoint struct {
	Version             int            `json:"version"`
	SessionID           string         `json:"session_id"`
	EpochID             int            `json:"epoch_id"`
	Until               int            `json:"until"`
	UntilSeconds        int            `json:"until_seconds"`
	MaxCostUSD          *float64       `json:"max_cost_usd"`
	Deadline            time.Time      `json:"deadline"`
	Result              *PVGResult     `json:"result"`
	RecoverableFailures int            `json:"recoverable_failures"`
	ReviewCycles        int            `json:"review_cycles"`
	TranscriptOffset    int64          `json:"transcript_offset"`
	TurnInFlight        bool           `json:"turn_in_flight,omitempty"`
	ResultID            string         `json:"result_id,omitempty"`
	Pending             *completedTurn `json:"pending,omitempty"`
}

// Pending is saved before processing an executor result. A crash during handoff
// or policy evaluation reprocesses this result without calling the agent again.
type completedTurn struct {
	Turn             TurnResult `json:"turn"`
	FinishedAt       time.Time  `json:"finished_at"`
	Stopped          bool       `json:"stopped"`
	TranscriptOffset int64      `json:"transcript_offset"`
	CompletionID     string     `json:"completion_id"`
}

func (p *PVG) checkpointPath() string {
	return filepath.Join(p.State.TurnsDir(), fmt.Sprintf("epoch-%04d", p.Config.EpochID), "checkpoint.json")
}

func (p *PVG) restoreCheckpoint() error {
	path := p.checkpointPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Existing turn files without a checkpoint cannot be resumed reliably.
		entries, readErr := os.ReadDir(filepath.Dir(path))
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		for _, entry := range entries {
			if entry.IsDir() {
				return recoveryErrorf("attempt %d has turn history but no saved execution checkpoint", p.Config.EpochID)
			}
		}
		p.checkpoint = &runCheckpoint{
			Version: 1, SessionID: p.State.SessionID, EpochID: p.Config.EpochID,
			Until: p.Config.Until, UntilSeconds: p.Config.UntilSeconds,
			MaxCostUSD: p.Config.MaxCostUSD, Deadline: p.runDeadline(), Result: p.Result,
		}
		if err := p.saveCheckpoint(); err != nil {
			return err
		}
		p.Evidence.Log("game_start", 0, "system", nil)
		return nil
	}
	if err != nil {
		return err
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return recoveryErrorf("decode execution checkpoint: %w", err)
	}
	r := checkpoint.Result
	if checkpoint.Version != 1 || checkpoint.SessionID != p.State.SessionID || checkpoint.EpochID != p.Config.EpochID || r == nil {
		return recoveryErrorf("execution checkpoint version or session/epoch identity does not match")
	}
	if r.SystemName != p.Compiled.Environment.Name || r.Rounds < 0 || r.ProverRounds != (r.Rounds+1)/2 || r.VerifierRounds != r.Rounds/2 ||
		checkpoint.RecoverableFailures < 0 || checkpoint.RecoverableFailures > maxRecoverableAgentFailures+1 ||
		checkpoint.ReviewCycles < 0 || checkpoint.ReviewCycles > r.VerifierRounds || checkpoint.TranscriptOffset < 0 ||
		r.TotalCostUSD < 0 || r.TotalInputTokens < 0 || r.TotalOutputTokens < 0 || r.TotalCacheReadTokens < 0 || r.TotalCacheCreateTokens < 0 ||
		(checkpoint.UntilSeconds > 0) == checkpoint.Deadline.IsZero() {
		return recoveryErrorf("execution checkpoint has inconsistent progress or budgets")
	}
	switch r.GameResult {
	case "", GameFailure, GameStopped:
		if r.VerifierConceded {
			return recoveryErrorf("execution checkpoint has inconsistent concession")
		}
	case GameSuccess:
		if !r.VerifierConceded || r.Rounds == 0 || r.Rounds%2 != 0 {
			return recoveryErrorf("execution checkpoint has inconsistent success")
		}
	default:
		return recoveryErrorf("execution checkpoint has unknown result %q", r.GameResult)
	}
	if r.GameResult != "" && checkpoint.ResultID == "" {
		return recoveryErrorf("execution checkpoint is missing its result identity")
	}
	if pending := checkpoint.Pending; pending != nil {
		if r.GameResult != "" || pending.CompletionID == "" || pending.TranscriptOffset < 0 || pending.FinishedAt.IsZero() || pending.Turn.Role != turnRole(r.Rounds+1) {
			return recoveryErrorf("execution checkpoint has inconsistent pending turn")
		}
	}
	p.checkpoint, p.Result = &checkpoint, r
	// Paths belong to the current session location, not a previous host's copy.
	r.EvidencePath, r.TranscriptPath = p.State.EvidencePath, p.State.TranscriptPath
	if r.WorkspaceCheckpointPath != "" {
		r.WorkspaceCheckpointPath = p.State.WorkspacePath
	}
	p.Config.Until, p.Config.UntilSeconds, p.Config.MaxCostUSD = checkpoint.Until, checkpoint.UntilSeconds, checkpoint.MaxCostUSD
	if r.GameResult == "" {
		p.logGameState("game_resume")
	}
	return nil
}

func recoveryErrorf(format string, args ...any) error {
	return &RecoveryError{Err: fmt.Errorf(format, args...)}
}

func (p *PVG) saveCheckpoint() error {
	data, err := json.Marshal(p.checkpoint)
	if err != nil {
		return err
	}
	path := p.checkpointPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// appendPendingTurn repairs an interrupted handoff without truncating the
// transcript: operator updates may have been appended while the worker was down.
func (p *PVG) appendPendingTurn() error {
	pending := p.checkpoint.Pending
	round := p.Result.Rounds + 1
	turn := pending.Turn
	turnID := fmt.Sprintf("epoch-%04d/%04d-%s", p.Config.EpochID, round, turn.Role)
	marker := fmt.Sprintf("<!-- telos completion %s -->", pending.CompletionID)
	if found, err := transcriptHasMarker(p.State.TranscriptPath, pending.TranscriptOffset, marker); err != nil {
		return err
	} else if found {
		return nil
	}
	return AppendTurnWithOptions(p.State.TranscriptPath, turn.Role, (round+1)/2, string(turn.Status),
		turn.Logs, &turn.Stats, turnID, turn.Error, AppendTurnOptions{
			IncludeStatus: true, CompletionMarker: marker,
			PiSessionPath: p.State.Turn(p.Config.EpochID, round, turn.Role).PiSessionPath(),
			EvidencePath:  p.State.EvidencePath,
		})
}

func transcriptHasMarker(path string, offset int64, marker string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() < offset {
		return false, recoveryErrorf("transcript is shorter than its execution checkpoint offset")
	}
	tail, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
	if err != nil {
		return false, err
	}
	if bytes.Contains(tail, []byte("\n"+marker+"\n")) {
		// A previous append may have reached the page cache before Sync failed.
		return true, f.Sync()
	}
	return false, nil
}

func turnRole(round int) string {
	if round%2 == 0 {
		return "verifier"
	}
	return "prover"
}
