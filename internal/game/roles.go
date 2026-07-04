package game

import "github.com/telos-org/telos/internal/protocol"

// Role and protocol-mode identifiers shared between the game loop (which sets
// them on TurnState and the task state machine) and the executor (which reads
// them to choose the per-turn output contract). Centralizing the strings keeps
// the writer and reader from drifting apart over an unchecked literal.
const (
	RoleProver   = protocol.RoleProver
	RoleVerifier = protocol.RoleVerifier

	// ProtocolModePVG is the default prover/verifier output contract: the
	// verifier emits a final <status> tag. ProtocolModeReview is the fixed
	// review-cycle contract: the verifier emits <review>/<summary> blocks and no
	// status tag.
	ProtocolModePVG    = protocol.ProtocolModePVG
	ProtocolModeReview = protocol.ProtocolModeReview
)
