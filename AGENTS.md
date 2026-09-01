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
