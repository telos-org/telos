package sessionapi

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/telos-org/telos/internal/evidence"
)

// EmitPendingEpochFinalization publishes the latest terminal epoch exactly
// once. The evidence writer deduplicates the deterministic finalization key,
// so a crash never requires a second acknowledgement in the manifest.
func EmitPendingEpochFinalization(sessionDir string) (bool, error) {
	manifest, err := ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return false, err
	}
	if len(manifest.Specs) == 0 {
		return false, nil
	}
	evidencePath := manifest.Specs[0].EvidencePath
	if evidencePath == nil || *evidencePath == "" {
		return false, nil
	}
	epoch := manifest.LastEpoch()
	if !epochNeedsFinalization(epoch) {
		return false, nil
	}

	specName := epoch.SpecName
	if specName == "" {
		specName = manifest.SpecName
	}
	key := epochFinalizationKey(manifest.SessionID, epoch.ID)
	writer := evidence.New(
		specName,
		*evidencePath,
		manifest.SessionID,
		epoch.ID,
	)
	appended, err := writer.LogEpochFinalized(
		ptrOr(epoch.RoundCount, 0),
		evidence.EpochFinalized{
			EpochID:          epoch.ID,
			SpecName:         epoch.SpecName,
			SpecVersion:      epoch.SpecVersion,
			Revision:         ptrOr(epoch.Revision, ""),
			PackageDigest:    ptrOr(epoch.PackageDigest, ""),
			SpecSHA256:       epoch.SpecSHA256,
			EpochStartedAt:   epoch.StartedAt,
			EpochFinishedAt:  *epoch.FinishedAt,
			Result:           *epoch.Result,
			GameResult:       epochGameResult(*epoch.Result),
			CompletionReason: ptrOr(epoch.CompletionReason, ""),
			VerifierConceded: ptrOr(epoch.VerifierConceded, false),
			CheckpointSaved:  ptrOr(epoch.CheckpointSaved, false),
			CheckpointPath:   ptrOr(epoch.CheckpointPath, ""),
			CheckpointBytes:  epoch.CheckpointBytes,
			FinalizationKey:  key,
			Error:            ptrOr(epoch.Error, ""),
		},
	)
	if err != nil {
		return false, fmt.Errorf("epoch %d: %w", epoch.ID, err)
	}
	return appended, nil
}

// repairEpochFinalization publishes only after the worker releases ownership,
// ensuring checkpoint metadata is complete before an API read repairs a crash.
func repairEpochFinalization(sessionDir string) (bool, error) {
	held, err := runnerLockHeld(sessionDir)
	if err != nil {
		return false, fmt.Errorf("inspect runner lock: %w", err)
	}
	if held {
		return false, nil
	}
	return EmitPendingEpochFinalization(sessionDir)
}

func epochNeedsFinalization(epoch *Epoch) bool {
	return epoch != nil &&
		epoch.FinishedAt != nil &&
		epoch.Result != nil &&
		epochSupportsFinalization(epoch)
}

func epochSupportsFinalization(epoch *Epoch) bool {
	return epoch != nil && slices.Contains(
		epoch.WorkerCapabilities,
		CapabilityEpochFinalizedEventsV1,
	)
}

func epochFinalizationKey(sessionID string, epochID int) string {
	return fmt.Sprintf("%s:epoch:%08d:finalized", sessionID, epochID)
}

func epochGameResult(result string) string {
	switch result {
	case "completed":
		return "success"
	case "stopped":
		return "stopped"
	default:
		return "failure"
	}
}
