---
title: Models
description: An overview of the LLM inference options exposed by Telos.
group: Platform
---

Telos is designed to be flexible and neutral to model choice. We support the following options

All model choice can be selected via the CLI (`--model <connection-name>/<model-id>`). A default can be in `telos config` or via the web UI. (TODO(grohan): impl the PUT to `/api/inference/preference`). Telos also exposes standard "thinking effort" values (`--thinking [low,medium,high,xhigh]`)


## Managed Inference

Telos exposes `telos/default` and `telos/max` as managed inference models. These abstract routing to frontier open and frontier closed models respectively.

The value of Telos-managed inference is to provide a cost-efficient, reliable option:

- Reliable inference (automatic provider rotation in case of downtime)
- Intelligent model routing (routes requests to different models based on the workload)
- Increased rate limits and prioritization.

## Bring Your Own Inference

Telos lets you bring your own inference, configurable at https://usetelos.ai/workspace?tab=inference

### API key

In many instances, teams have active API keys with major providers (Anthropic, OpenAI, xAI) or marketplaces (OpenRouter). Telos supports configuring this via the web interface.

### Subscription

For individual developers or small teams, most token spend goes through subscriptions like ChatGPT Plus/Pro. Telos supports connecting with the following subscription providers:

- ChatGPT
- xAI

As of October 2026, Anthropic is unsupported since connecting third-party applications to their subscription is against their terms of service.


TODO(grohan): mutability of model choice mid-goal?
