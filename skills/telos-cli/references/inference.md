---
title: Models and inference
description: Choose managed inference or a connected subscription for Cloud, and choose a pi model for local runs.
group: Platform
---

# Models and inference

Cloud Goals and local runs select models at different points:

| Execution | Selection |
| --- | --- |
| New Cloud session | Telos resolves a managed tier or connected subscription and retains it across revisions. |
| Local run | The local `pi` installation receives a provider and model for that run. |

## Cloud Goals

Telos provides two managed tiers:

```bash
telos apply SPEC.md --message "Launch with the selected model" --model telos/default --context CONTEXT
telos apply SPEC.md --message "Launch with the selected model" --model telos/max --context CONTEXT
```

`telos/default` is the standard managed tier. `telos/max` selects the larger
managed tier. Both are operated and billed by Telos, so they need no separate
provider setup.

Cloud can also use a ChatGPT or Grok subscription connected in the Telos app.
Once connected, it appears in `telos config`:

```console
$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         personal
Subscriptions
  MyChatGPT  chatgpt-codex  alice@example.com  connected
```

The first value is the user-chosen connection name. Combine it with a model as
`<connection-name>/<model-name>`:

```bash
telos apply SPEC.md --message "Launch with the selected model" --model MyChatGPT/gpt-5.5 --context CONTEXT
```

The selected connection must exist exactly once and report `connected`.
Connection creation and browser authorization happen in the Telos app; the CLI
uses connections already available to the selected context.

### Cloud selection order

A Cloud inference selection is fixed when the session is created. New sessions
use this selection order:

1. `--model` on `telos apply`
2. `TELOS_MODEL`
3. the selected context's workspace inference preference
4. Telos Default

When neither `--model` nor `TELOS_MODEL` supplies a model, the CLI sends no
selection. Cloud then uses the workspace preference, including a saved API-key
default. Choose that preference under **Inference** in the Telos app. A
workspace with no saved preference uses the standard managed tier.

The explicit Cloud model forms are `telos/default`, `telos/max`, and
`<connection-name>/<model-name>`.

Use `--model` for one deployment, or `TELOS_MODEL` for a terminal session or
script. An explicitly empty `--model` clears the environment override for that
command and lets Cloud use the workspace preference:

```bash
telos apply SPEC.md --message "Use the workspace model preference" --model "" --context CONTEXT
```

`telos config --model` is no longer supported. If your configuration file
contains `default_model` from an older CLI version, Telos ignores it and removes
it the next time the CLI saves that configuration. Your saved authentication
and context remain available.

Later revisions keep the session's existing inference configuration. Applying
with `--session` rejects an effective model selection from `--model` or
`TELOS_MODEL`. Omit the override, unset `TELOS_MODEL`, or pass `--model ""` to
keep the existing selection when updating the spec.

## Thinking effort

`--thinking` sets reasoning effort for both implementation and verification,
not a turn timeout. It works with managed and subscription inference; supported
levels depend on the model and provider.

```bash
telos apply SPEC.md --message "Launch with high thinking effort" --context CONTEXT --thinking high
telos run REPORT_SPEC.md --workspace . --until 3 --thinking high
```

`--thinking` overrides `TELOS_THINKING`. Otherwise, Cloud uses its service
default (currently `medium`), while local runs default to `high`.

Cloud thinking is fixed at creation: `apply --session` rejects a non-empty
override. Unset `TELOS_THINKING` or pass `--thinking ""` to keep the existing
setting when updating the spec.

## Local runs

A `platform: local` spec runs through the `pi` coding agent installed on the
same machine:

```bash
telos run REPORT_SPEC.md --workspace . --model openai-codex/gpt-5.5
```

Local model names use pi's `<provider>/<model-id>` form. Selection order is:

1. `--model` on `telos run`
2. `TELOS_MODEL`
3. `openai-codex/gpt-5.5`

Inside a running Telos worker, `TELOS_MODEL` defaults to that worker's selected
model, so nested runs use the same inference provider and model unless you
override it. Hosted child tasks submitted without a model through the Sessions
API use the deployment's selected model.

Provider authentication comes from the local pi installation; run `pi` and use
`/login` to configure it.
