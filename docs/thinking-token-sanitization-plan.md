# Plan: `<think>` / reasoning-token sanitization in the native harness

Date: 2026-06-15
Status: planned (not yet implemented)

## Problem

Per `evals/docs/silares-kimi-telos-findings.md` (run `df351670`), the Silares
Kimi runs leaked reasoning/control tokens into the **visible** transcript:
repeated `</think>` markers, narrated intended tool calls, and even non-English
narration bleeding into an English benchmark transcript. That text reaches the
Telos transcript, `game.ExtractStatus`, scoring, and the completion gate.

This is now the **native harness's** responsibility, not Pi's.

## Scope

The leak only exists on the **chat-completions** path. Confirmed in the code:

- `parseResponseOutput` (responses API) only collects `type=="message"` text;
  reasoning items are already dropped.
- `parseAnthropicOutput` only collects `type=="text"`; `thinking` blocks already
  dropped.
- `chatMessage` has only `Content` (no `reasoning_content`), so a *separate*
  reasoning field is already ignored.

What leaks: reasoning models (Kimi, DeepSeek-R1, QwQ) that emit
`<think>…</think>` **inline inside `content`** on the chat path.

## Design

### Single chokepoint

Add `sanitizeVisibleText` and call it in `agentLoop.run` immediately after
`transport.send`, before logging, the completion gate, and the final return.
Transports stay pure wire; the loop owns text → gate → log → return. The
function is a no-op when no tags are present, so it is safe to apply to every
provider defensively.

### Function

```go
// sanitizeVisibleText splits leaked reasoning out of a model's visible text.
// Returns the cleaned visible text and the removed reasoning (for forensics).
func sanitizeVisibleText(raw string) (clean string, reasoning string)
```

Tag set (case-insensitive, constant): `think`, `thinking`, `reasoning`.

Algorithm:

1. If no recognized tag is present, return `(raw, "")` unchanged (guard).
2. Remove balanced `<tag>…</tag>` blocks (multiline/dotall, loop until none to
   handle multiple/nested blocks). Accumulate removed text into `reasoning`.
3. Handle the malformed cases observed in the findings:
   - **stray closing tag** (`reasoning</think>answer`, no opener): drop
     everything up to and including the *last* stray close — the tail is the
     answer.
   - **stray opening tag** (`answer<think>tail…`, truncated, no closer): drop
     from the opener to end.
4. Trim surrounding whitespace.

### Preserve, don't destroy, the reasoning

Log the cleaned text as the visible assistant content **and** the removed
reasoning as a separate `sessionContent{Type: "thinking"}` block in the session
JSONL (the schema already supports arbitrary content types). Forensics keep
everything; `Logs` / scoring / `ExtractStatus` / the completion gate see only
clean text.

### Interactions

- `<status>…</status>` is preserved (we only strip think/thinking/reasoning), so
  `game.ExtractStatus` still works. A status tag buried *inside* `<think>` is
  correctly treated as not-final.
- An all-reasoning turn (`<think>only</think>` → empty) falls through to the
  existing empty-final retry, which is the desired behavior.

## Configuration

- On by default; the present-tag guard makes it a no-op otherwise.
- `TELOS_NATIVE_KEEP_REASONING=1` kill-switch to disable stripping for
  debugging (passes raw content through unchanged).

## Tests

- balanced `<think>x</think>answer` → `answer`
- stray close `reasoning</think>answer` → `answer`
- stray open / truncated `answer<think>tail` → `answer`
- multiple blocks + mixed case `<THINK>` / `<Thinking>`
- no tags → unchanged
- incidental `a < b` (not a tag) → unchanged
- all-reasoning → `""` (exercises empty-final retry)
- integration: chat response with `<think>…</think>…<status>CONCEDE</status>`
  yields clean logs, `game.StatusConcede`, and a `thinking` block in the
  session file
- kill-switch: `TELOS_NATIVE_KEEP_REASONING=1` leaves content untouched

## Estimated size

~80 lines + tests, self-contained in `loop.go` (or a new `sanitize.go`) plus a
`thinking` content block in `sessionlog.go`.

## Known limitations

- A task that legitimately discusses `<think>` tags as literal content (e.g., a
  prompt-engineering task) would have those stripped. Acceptable; could exempt
  fenced code blocks later if it ever matters.
