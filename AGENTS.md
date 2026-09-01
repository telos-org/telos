# Repository Instructions

## Keep CLI behavior and documentation together

`skills/telos-cli/` is the canonical user and agent documentation for the
Telos CLI. It ships in every Telos release, and `usetelos.ai/docs` renders the
version named by `/releases/latest/manifest.json`.

Any user-visible CLI change must update the relevant documentation in the same
pull request. This includes commands, flags, defaults, validation, output,
Cloud behavior, lifecycle semantics, and safety or approval boundaries.

- Update `skills/telos-cli/SKILL.md` when agent workflow or authorization
  guidance changes.
- Update the relevant `skills/telos-cli/references/*.md` page so the rendered
  Web guide matches the released CLI. New reference pages must be linked from
  `SKILL.md` to become part of the guide.
- Keep examples and warning text synchronized with the actual CLI behavior.
- Run `bazel build //skills:telos_cli_bundle` when the skill changes.
- State in the pull request when a Telos release must be published and promoted
  before the updated CLI or guide becomes available to users.

A user-facing CLI change is incomplete while its canonical documentation still
describes the old behavior.

## Write for the documentation's audience

The CLI documentation bundle serves two audiences. Do not mix their voices:

- `skills/telos-cli/SKILL.md` is agent-facing. Write direct instructions to the
  agent, including when it must explain a risk or obtain user approval.
- `skills/telos-cli/references/*.md` is customer-facing because these pages are
  rendered in the Web guide. Address the customer directly as "you." Explain
  what Telos does, what the customer will see, and what they can do next.
- Never put agent instructions such as "tell the user," "ask the user," "obtain
  approval," "present this warning," or "do not retry automatically" in a
  customer-facing reference. Move that policy to `SKILL.md` or rewrite it from
  the customer's perspective.
- Keep documented behavior consistent with the CLI and UI, but do not duplicate
  exact runtime warning or error text unless quoting it materially helps the
  customer. Prefer explaining when the message appears and the customer's next
  action.

Before committing a changed reference, read it as a customer. If a sentence
instructs an agent instead of helping the customer use Telos, move it to
`SKILL.md` or rewrite it.
