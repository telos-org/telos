package sessionupdate

import (
	"fmt"

	"github.com/telos-org/telos/internal/evidence"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

func ProjectSpecUpdate(event sessionapi.SpecUpdateEvent) {
	message := fmt.Sprintf(
		"The operator updated the session spec from version %d to %d. Reload the current spec before continuing.",
		event.PreviousSpecVersion,
		event.CurrentSpecVersion,
	)
	if event.TranscriptPath != "" {
		if err := game.InitializeTranscript(
			event.TranscriptPath,
			event.SessionID,
			event.SpecName,
			event.EvidencePath,
			event.SessionCreatedAt,
		); err == nil {
			_ = game.AppendExternalUpdate(event.TranscriptPath, game.ExternalUpdate{
				Message:               message,
				PreviousSpecVersion:   event.PreviousSpecVersion,
				CurrentSpecVersion:    event.CurrentSpecVersion,
				PreviousSpecSHA256:    event.PreviousSpecSHA256,
				CurrentSpecSHA256:     event.CurrentSpecSHA256,
				PreviousPackageDigest: event.PreviousPackageDigest,
				CurrentPackageDigest:  event.CurrentPackageDigest,
				SpecPath:              event.SpecPath,
			})
		}
	}
	if event.EvidencePath == "" {
		return
	}
	ev := evidence.New(event.SpecName, event.EvidencePath, event.SessionID, event.EpochID)
	if event.SessionCreatedAt != "" {
		ev.StartedAt = event.SessionCreatedAt
	}
	ev.Log("external_update", 0, "system", specUpdateEventData(message, event))
}

func specUpdateEventData(message string, event sessionapi.SpecUpdateEvent) map[string]interface{} {
	data := map[string]interface{}{
		"message":               message,
		"previous_spec_version": event.PreviousSpecVersion,
		"current_spec_version":  event.CurrentSpecVersion,
		"spec_path":             event.SpecPath,
	}
	if event.PreviousSpecSHA256 != "" {
		data["previous_spec_sha256"] = event.PreviousSpecSHA256
	}
	if event.CurrentSpecSHA256 != "" {
		data["current_spec_sha256"] = event.CurrentSpecSHA256
	}
	if event.PreviousPackageDigest != "" {
		data["previous_package_digest"] = event.PreviousPackageDigest
	}
	if event.CurrentPackageDigest != "" {
		data["current_package_digest"] = event.CurrentPackageDigest
	}
	return data
}
