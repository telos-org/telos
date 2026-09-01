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
telos apply SPEC.md --model telos/default --context CONTEXT
telos apply SPEC.md --model telos/max --context CONTEXT
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
Default model   workspace default
Subscriptions
  MyChatGPT  chatgpt-codex  alice@example.com  connected
```

The first value is the user-chosen connection name. Combine it with a model as
`<connection-name>/<model-name>`:

```bash
telos apply SPEC.md --model MyChatGPT/gpt-5.5 --context CONTEXT
```

The selected connection must exist exactly once and report `connected`.
Connection creation and browser authorization happen in the Telos app; the CLI
uses connections already available to the selected context.

### Cloud selection order

A Cloud inference selection is fixed when the session is created. The CLI
resolves an explicit selection in this order:

1. `--model` on `telos apply`
2. `TELOS_MODEL`
3. the machine-local value set by `telos config --model`

If all three are empty, the CLI sends no selection and Cloud uses the chosen
context's workspace inference preference. A workspace with no saved preference
uses the standard managed tier.

The Cloud forms are `telos/default`, `telos/max`, and
`<connection-name>/<model-name>`.

Set or clear the machine-local default with:

```bash
telos config --model telos/max
telos config --model ""
```

This value is not scoped per context. Clearing it means “defer to the workspace
preference.” The `workspace default` label printed by `telos config` does not
identify the model or subscription that the workspace will choose.

Later revisions keep the session's existing inference configuration. Applying
with `--session` rejects an effective model selection from `--model` or
`TELOS_MODEL`; the stored machine-local default is ignored for the update. An
explicit `--model ""` clears a non-empty environment override for that command.

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

The stored Cloud default from `telos config --model` does not participate in
local selection. Provider authentication comes from the local pi installation;
run `pi` and use `/login` to configure it.
