---
title: Models and inference
description: Choose managed inference or a connected subscription for Cloud, and choose a pi model for local runs.
group: Platform
---

# Models and inference

Cloud Goals and local runs select models at different execution boundaries.
Cloud resolves a managed tier or a subscription connection. Local runs pass a
provider and model directly to `pi`.

## Cloud Goals

Every new Cloud Goal receives an inference selection. Telos provides two
managed tiers:

```bash
telos apply SPEC.md --model telos/default
telos apply SPEC.md --model telos/max
```

`telos/default` is the standard managed tier. `telos/max` selects the larger
managed tier. Both are operated and billed by Telos, so they need no separate
provider setup.

A Cloud selection is made when the session is created. Later revisions keep
the session's existing inference configuration.

### Connected subscriptions

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

The first value is the connection name chosen by the user. Combine that name
with a model as `<connection-name>/<model-name>`:

```bash
telos apply SPEC.md --model MyChatGPT/gpt-5.5
```

The selected connection must exist exactly once and report `connected`.
Connection creation and browser authorization happen in the Telos app; the CLI
uses connections that are already available.

### Cloud selection order

For a new Cloud session, Telos resolves the model in this order:

1. `--model` on `telos apply`
2. `TELOS_MODEL`
3. the stored value set by `telos config --model`
4. the standard managed tier

The Cloud forms are `telos/default`, `telos/max`, and
`<connection-name>/<model-name>`.

Set or clear the stored Cloud default with:

```bash
telos config --model telos/max
telos config --model ""
```

`telos config` prints `workspace default` when no value is stored.

## Local runs

A `platform: local` spec runs through the `pi` coding agent installed on the
same machine:

```bash
telos run SPEC.md --workspace . --model openai-codex/gpt-5.5
```

Local model names use pi's `<provider>/<model-id>` form. Selection order is:

1. `--model` on `telos run`
2. `TELOS_MODEL`
3. `openai-codex/gpt-5.5`

The stored Cloud default from `telos config --model` does not participate in
local selection. Provider authentication comes from the local pi installation;
run `pi` and use `/login` to configure it.
