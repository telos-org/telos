---
title: Models
description: An overview of the LLM inference options exposed by Telos.
group: Platform
---

Telos is designed to be flexible and neutral to model choice, and supports the options below.

You can select a model from the CLI with `--model <name>/<model-id>`, where `<name>` is `telos` for managed inference or the name you gave your API key or subscription. A default can be set in the [web UI](https://usetelos.ai/workspace?tab=inference). Telos also exposes standard "thinking effort" values (`--thinking [low,medium,high,xhigh]`).

```console
$ telos apply SPEC.md --model telos/max --thinking high
$ telos apply SPEC.md --model my-anthropic-key/claude-sonnet-5-5
```

The model and thinking effort are set when a Goal is created. They can't be changed on an existing Goal, so `--model` and `--thinking` don't work with `--goal`.

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
