# Execution Hardening: Phase 0 Decisions

Companion to `docs/zero-vs-telos-execution-report.md`. These are the invariant
decisions that report asks for before implementation, plus scope additions.

## Decisions

1. **Native executor containment**: Containment is a declared, recorded config
   field (`ContainmentMode`: `container`, `session-workspace`, `uncontained`).
   Executor fails closed when started through sessionrun/daemon paths without a
   declared containment mode. Local developer CLI use may pass an explicit
   allow-uncontained option; the chosen mode is recorded in config, on every
   tool event, and in the session log.
2. **Log privacy**: Dual streams. Redacted events are the default for session
   API, SSE, and CLI surfaces. Raw verbatim JSONL is retained only under
   explicit opt-in (config/env). Tool schemas carry redaction metadata so the
   registry envelope can redact uniformly.
3. **Workspace model**: Single root scope. All native file/search/patch/bash
   paths resolve through one symlink-aware scoped resolver rooted at the
   session workspace. No multi-root scopes for now.
4. **Completion contract**: Structured terminal state enum recorded in the
   session log and returned from native execution:
   `completed`, `incomplete`, `exhausted`, `interrupted`, `policy_blocked`,
   `provider_failed`, `tool_failed`. Headless/controller flows must branch on
   the enum, never on the presence of assistant text.
5. **Provider roadmap**: Near-term multi-provider. A provider-neutral stream
   event contract is introduced in this pass, with the existing OpenAI
   Responses client moved behind it.

## BYO provider scope (addition to the report)

Local BYO mode supports:

- OpenAI-compatible Responses API (base URL + API key) — existing.
- Anthropic Messages-compatible API (base URL + API key).
- Gemini generateContent-compatible API (base URL + API key).
- Codex (ChatGPT Plus/Pro/Business/Enterprise) subscription via the public
  codex CLI OAuth client identity and the
  `chatgpt.com/backend-api/codex/responses` backend, mirroring Zero's
  `chatgpt` OAuth preset and `codex_responses` adapter.

Claude Code subscription auth is out of scope: Zero (the reference
implementation reviewed) has no Claude/Anthropic subscription OAuth preset,
and this pass only adopts subscription flows Zero already ships.
