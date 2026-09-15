---
title: Models and inference
description: Discover Cloud models, choose managed inference, a subscription, or a saved API key, and set the workspace default.
group: Platform
---

# Models and inference

Cloud Goals select managed inference, a connected subscription, or a saved API
key when the session is created. Local runs select a provider and model from
the local `pi` installation.

## Inspect Cloud inference

Add and manage connections under **Inference** in your workspace in the Telos
app. Cloud supports ChatGPT and Grok subscriptions, plus API keys for OpenAI,
Anthropic, OpenRouter, and xAI. The CLI uses saved connections; it does not
accept provider secrets or start provider authorization.

```console
$ telos config
Config file      ~/.telos/config.yaml
Endpoint         https://api.usetelos.ai
Authentication   valid
Context          personal
Workspace model  telos/default
Connections
  MyChatGPT       Subscription  connected
  Work Anthropic  API key       saved
```

API keys are labeled `saved`; that label does not claim their provider access
has been tested. `TELOS_MODEL` and `TELOS_THINKING` overrides appear separately
from the workspace default. `telos config --json` returns structured settings,
including connection IDs and account details, without credentials. Config
inspection reports authentication and lookup errors in its output; it is not
an authentication success exit-code check.

## Discover models

List available models with copyable deployment options:

```bash
telos config --models
telos config --models --json
telos config --models --refresh
```

The listing groups models by connection and includes the managed
`telos/default` and `telos/max` choices. `--refresh` refreshes API-key catalogs
and reloads the subscription catalog. Forcing an API-key refresh requires
workspace operator access.

Errors are reported per connection without hiding other results. Cached models
from a failed refresh are marked `stale`. The command exits unsuccessfully if
any part of the listing failed; JSON output still contains the results and
errors. Deployment selection requires a successful catalog check for the
chosen connection and model.

## Override one new deployment

For a subscription or API key, use `<connection-name>/<model-id>`. Copy a
selection from the model listing and quote names containing spaces:

```bash
telos apply SPEC.md --context CONTEXT --model MyChatGPT/gpt-5.5
telos apply SPEC.md --context CONTEXT --model "Work Anthropic/MODEL_ID"
```

Replace `MODEL_ID` with an available model ID. The CLI determines whether the
named connection is a subscription or API key. Names are case-sensitive and
must identify exactly one connection. A subscription must report `connected`,
and the model must be available to the selected connection. Explicit selections
are checked before the CLI publishes a spec package.

To use managed inference:

```bash
telos apply SPEC.md --context CONTEXT --model telos/default
telos apply SPEC.md --context CONTEXT --model telos/max
```

Both managed tiers are operated and billed by Telos and need no provider
connection. All these options also work with a published package:

```bash
telos apply @scope/package:version --context CONTEXT \
  --model "Work Anthropic/MODEL_ID" --thinking high
```

### Select by connection ID

If names collide or a name contains `/`, use a stable connection reference
from `telos config --json` or `telos config --models --json` and an explicit
raw model ID:

```bash
telos apply SPEC.md --context CONTEXT \
  --connection-id api-key:KEY_ID --model MODEL_ID
telos apply SPEC.md --context CONTEXT \
  --connection-id subscription:CONNECTION_ID --model MODEL_ID
```

The `api-key:` and `subscription:` prefixes inspect only the chosen connection
type, so an unrelated discovery outage does not block selection. An unqualified
connection ID also works when both connection lists are available and the ID
is unique. Model IDs containing `/`, such as OpenRouter's provider-prefixed IDs,
are preserved. With `--connection-id`, supply `--model` explicitly; it does not
come from `TELOS_MODEL`.

## Set the workspace default

`--workspace-model` updates the shared default for future deployments from both
the CLI and web. It requires workspace operator access:

```bash
telos config --workspace-model "Work Anthropic/MODEL_ID"
telos config --workspace-model telos/default
telos config --connection-id api-key:KEY_ID --workspace-model MODEL_ID --json
```

Existing deployments retain their saved inference. This command does not
change your local config file or save a thinking preference. An active
`TELOS_MODEL` still overrides the workspace preference for CLI deployments.

The command uses the selected context. To target another workspace for a
single config operation, set `TELOS_CONTEXT`:

```bash
TELOS_CONTEXT=@team-handle telos config --models
TELOS_CONTEXT=@team-handle telos config --workspace-model telos/max
```

`telos config --context @team-handle` retains its separate meaning: it changes
the saved CLI context. It cannot be combined with `--models`, `--refresh`, or
`--workspace-model`. See [Cloud authentication](cloud.md#choose-the-context).

### Selection order

New Cloud deployments use:

1. `--model` on `telos apply`
2. `TELOS_MODEL`
3. the selected context's workspace inference preference
4. Telos Default

With no model override, the CLI omits the inference selection and Cloud resolves
the workspace preference. An explicitly empty `--model` suppresses an
environment override for that command:

```bash
telos apply SPEC.md --context CONTEXT --model ""
```

`telos config --model` is no longer supported. If an older configuration file
contains `default_model`, Telos ignores it and removes it the next time the
CLI saves that configuration. Saved authentication and context are retained.

## Thinking effort

`--thinking` requests reasoning effort for both implementation and verification,
not a turn timeout. It works with managed, subscription, and API-key inference.
Supported levels and how the requested effort is applied depend on the model
and provider.

```bash
telos apply SPEC.md --context CONTEXT --thinking high
telos run REPORT_SPEC.md --workspace . --until 3 --thinking high
```

`--thinking` overrides `TELOS_THINKING`. Otherwise, Cloud uses its service
default (currently `medium`), while local runs default to `high`. A receipt
shows the requested effort; it does not claim a provider used that exact level.

## Update and inspect a deployment

Inference is fixed when a Cloud session is created. Later spec revisions keep
the saved connection, model, and thinking effort. `apply --session` rejects
non-empty model or thinking overrides, including environment overrides:

```bash
telos apply SPEC.md --session SESSION_ID --context CONTEXT \
  --model "" --thinking ""
```

The empty flags clear environment overrides for that invocation. They do not
reset the existing deployment to the workspace preference.

Deployment receipts and `telos describe` show the saved inference connection,
model, and requested thinking effort when Cloud returns those fields:

```bash
telos describe SESSION_ID --context CONTEXT
telos describe SESSION_ID --context CONTEXT --json
```

## Local runs

A `platform: local` spec uses the local `pi` coding agent and its credentials:

```bash
telos run REPORT_SPEC.md --workspace . --until 3 \
  --model openai-codex/gpt-5.5
```

Local names use Pi's `<provider>/<model-id>` form. Selection order is `--model`,
then `TELOS_MODEL`, then `openai-codex/gpt-5.5`. Cloud connection names and
`--connection-id` do not configure local credentials. Run `pi` and use `/login`
to configure them.

Inside a Telos worker, `TELOS_MODEL` defaults to that worker's selected model.
Nested runs inherit that model unless overridden. Hosted child tasks submitted
without a model through the Sessions API use the deployment's selected model.
