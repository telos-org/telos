---
title: Models and inference
description: Choose managed Telos inference or connect an existing ChatGPT or Grok subscription.
group: Platform
---

# Models and inference

Every Cloud Goal runs on an inference connection. Telos supplies one by
default; the user can instead connect a subscription they already pay for.

## Managed tiers

Two managed models need no setup and bill through Telos:

```bash
telos apply SPEC.md --model telos/default
telos apply SPEC.md --model telos/max
```

`telos/default` is what a Goal uses when nothing selects a model.

## A user's own subscription

Telos can drive an existing ChatGPT account (OpenAI Codex models) or Grok
account (xAI models) instead. Work then bills against that subscription rather
than Telos.

**Connecting is a browser action, and an agent cannot do it.** There is no
`telos` command that connects a subscription. When a user wants one, stop and
tell them to connect it in the Telos app; then continue.

Once connected, `telos config` lists it:

```console
$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         @telos
Default model   workspace default
Subscriptions
  ChatGPT  chatgpt-codex    connected
```

The columns are connection name, provider, account, and status. **The first
column is the name to use** — it is a label the user chose, not the provider
id. Select a model on that connection as `<connection-name>/<model-name>`:

```bash
telos apply SPEC.md --model ChatGPT/gpt-5.5
```

The connection must report `connected`. A name that does not match, or matches
more than one connection, is an error rather than a fallback.

## Where a model is chosen

`--model` accepts exactly three forms: `telos/default`, `telos/max`, or
`<connection-name>/<model-name>`. Anything else is rejected.

Resolution order, highest first:

1. `--model` on `telos apply` or `telos run`
2. `$TELOS_MODEL`
3. the stored default from `telos config --model`
4. `telos/default`

Set the stored default for new Cloud deployments with:

```bash
telos config --model telos/max
telos config --model ""      # clear it
```

Read the current value from the `Default model` row of `telos config`;
`workspace default` there means nothing is stored and rule 4 applies.

## Local runs

`telos run` on a `platform: local` spec uses the `pi` runtime and the model
provider authenticated on that machine. Managed Cloud deployments never use a
workstation's local model credentials.
