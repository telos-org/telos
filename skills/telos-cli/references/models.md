---
title: Models
description: An overview of the LLM inference options exposed by Telos.
group: Platform
---

Telos is designed to be flexible and neutral to model choice, and supports the options below.

You can select a model from the CLI with `--model <name>/<model-id>`, where `<name>` is `telos` for managed inference or the name you gave your API key or subscription. A default can be set in `telos config` or in the web UI (TODO(grohan): impl the PUT to `/api/inference/preference`). Telos also exposes standard "thinking effort" values (`--thinking [low,medium,high,xhigh]`).

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

For individual developers or small teams, most token spend goes through subscriptions like ChatGPT Plus/Pro. Telos supports connecting with the following subscription providers:

- ChatGPT
- xAI

As of October 2026, Anthropic is unsupported since connecting third-party applications to their subscription is against their terms of service.

TODO(grohan): mutability of model choice mid-goal?
