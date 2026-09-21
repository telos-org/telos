---
title: Models and inference
description: Use the workspace default or select managed inference, a subscription, or a saved API-key connection for one deployment.
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

## Override one new deployment

For a subscription or API key, use `<connection-name>/<model-id>`. Choose a
model available to that connection under **Inference** in the Telos app, and
quote names containing spaces:

```bash
telos apply SPEC.md --context CONTEXT --model MyChatGPT/gpt-5.5
telos apply SPEC.md --context CONTEXT --model "Work Anthropic/MODEL_ID"
```

Replace `MODEL_ID` with an available model ID. The CLI determines whether the
named connection is a subscription or API key. Names are case-sensitive, and
the selection must identify exactly one connection. If names make the
selection ambiguous, rename the connections in the app. Model IDs containing
`/`, such as OpenRouter's provider-prefixed IDs, are preserved. A subscription
must report `connected`, and the model must be available to the selected
connection. Explicit selections are checked before the CLI publishes a spec
package. If a connection or model cannot be checked, the command stops with
an error.

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

## Use the workspace default

Set the shared default under **Inference** in the Telos app. It applies to
future CLI and web deployments in that workspace. `telos config` displays it;
existing deployments retain their saved inference.

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
then `TELOS_MODEL`, then `openai-codex/gpt-5.5`. Local credentials come from Pi;
run `pi` and use `/login` to configure them.

Inside a Telos worker, `TELOS_MODEL` defaults to that worker's selected model.
Nested runs inherit that model unless overridden. Hosted child tasks submitted
without a model through the Sessions API use the deployment's selected model.
