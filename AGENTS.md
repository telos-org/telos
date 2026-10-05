# Repository Instructions

## The CLI guide is written by hand

`skills/telos-cli/` is the canonical user and agent documentation for the
Telos CLI. It ships in every Telos release, and `usetelos.ai/docs` renders the
version named by `/releases/latest/manifest.json`.

Do not add, edit, or delete anything under `skills/telos-cli/` unless the
person you are working with asks you to change that specific text. Do not draft
replacement prose for it, in the repository or in a pull request.

## Flag documentation impact

When a pull request changes user-visible CLI behavior, add a **Documentation
impact** section to its description. User-visible behavior includes commands,
flags, defaults, validation, output, Cloud behavior, lifecycle semantics, and
safety or approval boundaries. List:

- each page and section the change makes inaccurate, by path and heading;
- the sentence or example that is now wrong, quoted;
- what is now true, as plain facts rather than replacement prose.

A user-visible change is not ready to release until the guide matches it. State
in the pull request when a Telos release must wait for a documentation update,
and when a release must be published and promoted before the change reaches
users.

## When you are asked to edit the guide

- Run `bazel build //skills:telos_cli_bundle` after changing the skill.
- `SKILL.md` must link every reference page, and every page it links must
  exist. The Web guide only renders linked pages, and one missing page breaks
  the entire guide.

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
