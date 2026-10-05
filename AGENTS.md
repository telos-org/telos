# Repository Instructions

## The CLI guide is written by hand

`skills/telos-cli/` is the Telos CLI guide. It ships with every release and is
rendered at `usetelos.ai/docs`.

Do not edit anything under `skills/telos-cli/` unless you are asked to change
that specific text, and do not draft replacement prose for it.

## Flag documentation impact

When a pull request changes anything a CLI user can see, including validation
and output, add a **Documentation impact** section to its description:

- each page and section the change makes inaccurate, by path and heading;
- the sentence or example that is now wrong, quoted;
- what is now true, as plain facts.

The change is not ready to release until the guide matches it.

## Editing the guide

When you are asked to edit it:

- `SKILL.md` is for agents: direct instructions, including when to explain a
  risk or get approval. `references/*.md` is for customers: address them as
  "you", and never instruct an agent there.
- `SKILL.md` must link every reference page, and every linked page must exist.
  One missing page breaks the whole Web guide.
- Run `bazel build //skills:telos_cli_bundle`.
