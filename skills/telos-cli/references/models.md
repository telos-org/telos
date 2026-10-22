---
title: Models
description: An overview of the LLM inference options exposed by Telos.
group: Platform
---

Telos is designed to be flexible and neutral to model choice, and supports the options below.

You can select a model from the CLI with `--model <name>/<model-id>`, where `<name>` is `telos` for managed inference or the name you gave your API key or subscription. A default can be set in the [web UI](https://usetelos.ai/workspace?tab=inference). For new Goals, `--thinking` accepts `low`, `medium`, `high`, or `xhigh`.

```console
$ telos apply SPEC.md --model telos/max --thinking high
$ telos apply SPEC.md --model my-anthropic-key/claude-sonnet-5-5
```

To change an existing Goal's model or thinking effort, use `--goal` without a spec file:

```console
$ telos apply --goal goal_c7d2f0a4e8 --model telos/max --thinking high
```

The running turn finishes with its current settings. The change takes effect at the next prover or verifier turn; an idle Goal waits for its next scheduled or triggered turn.

You can also switch between managed inference, API keys, and subscriptions by selecting the new connection's name. Connection switching is available only on Goals whose runtime and host support it. Existing children keep their connection; new children inherit the parent's confirmed settings. Model, thinking, and connection take effect together at the next turn.

```console
$ telos apply --goal goal_c7d2f0a4e8 --model "Work OpenAI/gpt-5"
```

Switching between different credentials for the same provider route is not supported. Previous credentials remain available to running turns and existing children until the Goal is deleted.

Settings updates accept thinking levels supported by the selected model. If you change the model without specifying thinking, Pi may lower the current level to one the new model supports. An explicitly requested level must be supported.

If the Goal's runtime session is recreated, retry the change with both `--model` and `--thinking`.

## Managed inference

Telos exposes `telos/default` and `telos/max` as managed inference models. These abstract routing to frontier open and frontier closed models, respectively.

The value of Telos-managed inference is to provide a cost-efficient, reliable option:

- Reliable inference (automatic provider rotation in case of downtime)
- Intelligent model routing (routes requests to different models based on the workload)
- Increased rate limits and prioritization

## Bring your own inference

Telos lets you bring your own inference, configurable at <https://usetelos.ai/workspace?tab=inference>.

### API key

In many instances, teams have active API keys with major providers (Anthropic, OpenAI, xAI) or marketplaces (OpenRouter). Telos supports configuring this via the web interface.

### Subscription

Many individual developers and small teams already pay for subscriptions like ChatGPT Plus/Pro. Telos supports connecting with the following subscription providers:

- ChatGPT
- xAI

As of October 2026, Anthropic is unsupported since connecting third-party applications to their subscription is against their terms of service.

## Spend observability

In order to see costs per Goal on inference, run either `telos describe` or `telos list --wide`. For example:

```console
$ telos describe goal_c7d2f0a4e8
...
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Cloud     $1.25 (compute $1.00, storage $0.25)
Inference $3.40 (Telos)
```

Costs are labeled by the provider: `Telos`, the provider of your API key (like `Anthropic API key`), or your subscription (like `ChatGPT subscription`). Telos bills only `Telos` inference; your provider bills API key usage, and your subscription covers its own. For subscription inference, costs displayed are the API-equivalent cost.

For an API key or subscription, the agent runtime (`Pi`-backed) records the inference cost. If the runtime can't be reached, that cost is displayed as `unavailable`.
