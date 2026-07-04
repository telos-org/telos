package executor

// envKnobs is the set of executor-internal tuning values read from the
// environment. They are orthogonal to per-session budgets (which live in the
// manifest and win over these) and to model properties (which live in the
// capability profile). Every knob here has no manifest equivalent; it is an
// operator escape hatch for the harness itself.
//
// Resolve once per turn via resolveEnvKnobs and thread the result through
// newNativeTools / newAgentLoop so every call site agrees, and so the resolved
// values can be logged for auditability.
type envKnobs struct {
	// ToolMaxBytes caps the byte size of a single tool's text output.
	ToolMaxBytes int
	// ToolMaxLines caps the line count of a single tool's text output.
	ToolMaxLines int
	// KeepReasoning disables reasoning-tag stripping from visible output.
	KeepReasoning bool
}

func resolveEnvKnobs() envKnobs {
	return envKnobs{
		ToolMaxBytes:  envInt("TELOS_TOOL_MAX_BYTES", defaultToolMaxBytes, 16, "TELOS_NATIVE_TOOL_MAX_BYTES"),
		ToolMaxLines:  envInt("TELOS_TOOL_MAX_LINES", defaultToolMaxLines, 1, "TELOS_NATIVE_TOOL_MAX_LINES"),
		KeepReasoning: envBool("TELOS_KEEP_REASONING", "TELOS_NATIVE_KEEP_REASONING"),
	}
}
