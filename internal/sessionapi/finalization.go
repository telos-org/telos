package sessionapi

import (
	"fmt"
	"path/filepath"

	"github.com/telos-org/telos/internal/evidence"
)

// BindEpochFinalizationIdentity snapshots the desired spec identity onto a new
// epoch before it can run or become terminal. Callers must not use it to rebind
// an existing epoch: current session state may have advanced since that epoch
// started.
func BindEpochFinalizationIdentity(manifest *Manifest, epoch *Epoch) {
	if manifest == nil || epoch == nil {
		return
	}
	epoch.SpecName = manifest.SpecName
	epoch.SpecVersion = cloneInt(manifest.CurrentSpecVersion)
	epoch.Revision = cloneString(manifest.CurrentRevision)
	epoch.PackageDigest = cloneString(manifest.PackageDigest)
	epoch.SpecSHA256 = specSHA256ForVersion(
		manifest.SpecVersions,
		manifest.CurrentSpecVersion,
	)
	epoch.FinalizationKey = fmt.Sprintf(
		"%s:epoch:%08d:finalized",
		manifest.SessionID,
		epoch.ID,
	)
	if !containsString(
		epoch.WorkerCapabilities,
		CapabilityEpochFinalizedEventsV1,
	) {
		epoch.WorkerCapabilities = append(
			epoch.WorkerCapabilities,
			CapabilityEpochFinalizedEventsV1,
		)
	}
}

// EmitFinalizedEpochEventsFromDisk drains the durable manifest-to-event outbox
// on behalf of the epoch owner. The owner calls it only after checkpoint and
// terminal metadata are fully persisted.
func EmitFinalizedEpochEventsFromDisk(sessionDir string) (bool, error) {
	manifest, err := ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return false, err
	}
	return EmitFinalizedEpochEvents(sessionDir, manifest)
}

// EmitFinalizedEpochEvents emits every fully bound terminal epoch that
// has not yet cleared its durable outbox marker. The bool reports whether the
// latest epoch was repaired, allowing a restarted worker to return that result
// without starting another agent cycle.
func EmitFinalizedEpochEvents(
	sessionDir string,
	manifest *Manifest,
) (bool, error) {
	if manifest == nil || len(manifest.Specs) == 0 {
		return false, nil
	}
	evidencePath := manifest.Specs[0].EvidencePath
	if evidencePath == nil || *evidencePath == "" {
		return false, nil
	}
	repairedLatest := false
	for i := range manifest.Epochs {
		epoch := &manifest.Epochs[i]
		if !epochFinalizationPending(epoch) {
			continue
		}
		specName := epoch.SpecName
		if specName == "" {
			specName = manifest.SpecName
		}
		writer := evidence.New(
			specName,
			*evidencePath,
			manifest.SessionID,
			epoch.ID,
		)
		_, err := writer.LogEpochFinalized(
			intValue(epoch.RoundCount),
			evidence.EpochFinalized{
				EpochID:          epoch.ID,
				SpecName:         epoch.SpecName,
				SpecVersion:      epoch.SpecVersion,
				Revision:         stringValue(epoch.Revision),
				PackageDigest:    stringValue(epoch.PackageDigest),
				SpecSHA256:       epoch.SpecSHA256,
				EpochStartedAt:   epoch.StartedAt,
				EpochFinishedAt:  *epoch.FinishedAt,
				Result:           *epoch.Result,
				GameResult:       epochGameResult(*epoch.Result),
				CompletionReason: stringValue(epoch.CompletionReason),
				VerifierConceded: boolValue(epoch.VerifierConceded),
				CheckpointSaved:  boolValue(epoch.CheckpointSaved),
				CheckpointPath:   stringValue(epoch.CheckpointPath),
				CheckpointBytes:  epoch.CheckpointBytes,
				FinalizationKey:  epoch.FinalizationKey,
				Error:            stringValue(epoch.Error),
			},
		)
		if err != nil {
			return false, fmt.Errorf("epoch %d: %w", epoch.ID, err)
		}
		if err := markFinalizationEventEmitted(
			sessionDir,
			epoch.ID,
			epoch.FinalizationKey,
		); err != nil {
			return false, fmt.Errorf("mark epoch %d finalization: %w", epoch.ID, err)
		}
		epoch.FinalizationEventEmitted = true
		if i == len(manifest.Epochs)-1 {
			repairedLatest = true
		}
	}
	return repairedLatest, nil
}

// RepairFinalizedEpochEvents drains the outbox only when no worker owns the
// session. This keeps an API reader or supervisor from publishing terminal
// evidence before a cooperative worker has persisted its final checkpoint
// metadata.
func RepairFinalizedEpochEvents(sessionDir string) (bool, error) {
	held, err := runnerLockHeld(sessionDir)
	if err != nil {
		return false, fmt.Errorf("inspect runner lock: %w", err)
	}
	if held {
		return false, nil
	}
	return EmitFinalizedEpochEventsFromDisk(sessionDir)
}

func epochFinalizationPending(epoch *Epoch) bool {
	return epoch != nil &&
		!epoch.FinalizationEventEmitted &&
		epoch.FinalizationKey != "" &&
		epoch.FinishedAt != nil &&
		epoch.Result != nil
}

func markFinalizationEventEmitted(
	sessionDir string,
	epochID int,
	key string,
) error {
	_, err := MutateManifest(
		filepath.Join(sessionDir, "session.json"),
		func(manifest *Manifest) error {
			for i := range manifest.Epochs {
				epoch := &manifest.Epochs[i]
				if epoch.ID == epochID && epoch.FinalizationKey == key {
					epoch.FinalizationEventEmitted = true
					return nil
				}
			}
			return fmt.Errorf("epoch %d finalization identity changed", epochID)
		},
	)
	return err
}

func specSHA256ForVersion(
	versions []map[string]any,
	version *int,
) string {
	if version == nil {
		return ""
	}
	for _, candidate := range versions {
		if numericMapValue(candidate, "version") == *version {
			value, _ := candidate["spec_sha256"].(string)
			return value
		}
	}
	return ""
}

func numericMapValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
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

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
