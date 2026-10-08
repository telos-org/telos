import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { isDeepStrictEqual } from "node:util";
import { getSupportedThinkingLevels } from "@earendil-works/pi-ai";
import { getBuiltinModels } from "@earendil-works/pi-ai/providers/all";

export default function (pi) {
  const file = process.env.TELOS_PI_MODEL_CONFIG;
  function providerConfig(provider, definition, currentModels = [], registeredModels = []) {
    const modelsFile = join(process.env.PI_CODING_AGENT_DIR || join(homedir(), ".pi", "agent"), "models.json");
    const models = existsSync(modelsFile) ? JSON.parse(readFileSync(modelsFile, "utf8")) : {};
    const existing = models.providers?.[provider] || {};
    const definitions = new Map(getBuiltinModels(provider).map((model) => [
      model.id, { ...model, baseUrl: existing.baseUrl ?? model.baseUrl },
    ]));
    // Extension models require complete metadata; models.json allows partial
    // entries. Use the pinned Pi defaults while retaining known model metadata.
    function normalize(model, baseUrl = existing.baseUrl) {
      const baseline = definitions.get(model.id);
      return {
        name: model.id, reasoning: false, input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 128000, maxTokens: 16384,
        ...baseline, ...model,
        api: model.api ?? existing.api ?? baseline?.api,
        baseUrl: model.baseUrl ?? baseUrl ?? baseline?.baseUrl,
      };
    }
    for (const model of [...(existing.models || []), ...registeredModels]) {
      definitions.set(model.id, normalize(model));
    }
    for (const model of currentModels.filter((model) => model.provider === provider && model.api !== "pi-virtual")) {
      // Catalog entries omit extension-only fields such as request headers.
      const original = definitions.get(model.id);
      definitions.set(model.id, { ...original, ...model, headers: model.headers ?? original?.headers });
    }
    const config = { ...existing, api: definition.api ?? existing.api };
    if (provider === "openrouter" && (!existing.baseUrl || ["https://openrouter.ai/api", "https://openrouter.ai/api/v1"].includes(existing.baseUrl.replace(/\/$/, "")))) {
      config.baseUrl = config.api === "anthropic-messages" ? "https://openrouter.ai/api" : "https://openrouter.ai/api/v1";
      config.authHeader = existing.authHeader ?? true;
    }
    definitions.set(definition.id, normalize(definition, config.baseUrl));
    config.models = [...definitions.values()];
    return config;
  }
  function liveDefinition(registry, provider, definition) {
    if (registry.getRegisteredNativeProvider(provider)) {
      throw new Error("Model metadata cannot replace an extension's native provider");
    }
    const current = registry.find(provider, definition.id);
    const registered = registry.getRegisteredProviderConfig(provider);
    let restoredConfig;
    if (current) {
      const changed = Object.keys(definition).filter((key) => !isDeepStrictEqual(definition[key], current[key]));
      if (changed.length) {
        throw new Error(`Cannot change model metadata during a live Pi invocation (${provider}/${definition.id}: ${changed.join(", ")}); no settings were changed`);
      }
      // A matching descriptor is saved for restart. It must rebuild the same
      // metadata without relying on an extension's current in-memory models.
      restoredConfig = providerConfig(provider, definition);
      const restored = restoredConfig.models.find((model) => model.id === definition.id);
      const live = providerConfig(provider, definition, registry.getAll(), registered?.models).models.find((model) => model.id === definition.id);
      const metadata = (model) => Object.fromEntries(Object.entries(model)
        .filter(([key, value]) => key !== "provider" && key !== "type" && value !== undefined));
      if (!isDeepStrictEqual(metadata(restored), metadata({ ...current, headers: live.headers }))) {
        throw new Error("Model metadata cannot preserve the existing model's configuration on restart; no settings were changed");
      }
    }
    const config = current ? undefined : providerConfig(provider, definition, registry.getAll(), registered?.models);
    const effective = config ?? restoredConfig;
    if (registered?.streamSimple && effective?.api !== undefined && effective.api !== registered.api) {
      throw new Error("Model metadata cannot change an extension's streaming API during a live Pi invocation");
    }
    return { model: current, config };
  }
  // Pi acknowledges slash commands even when their handler throws. Send the
  // actual result separately so an acknowledgement cannot hide a rejection.
  function reply(ctx, id, data) {
    ctx.ui.setStatus("telos-internal-settings", JSON.stringify({ id, success: true, data }));
  }
  function register() {
    if (!file) return;
    const { provider, definition } = JSON.parse(readFileSync(file, "utf8"));
    if (!definition) return;
    pi.registerProvider(provider, providerConfig(provider, definition));
  }
  register();
  pi.registerCommand("telos-internal-refresh-model", {
    description: "Refresh the deployment's model definition",
    handler: async (args, ctx) => {
      const { id, provider, definition } = JSON.parse(args);
      try {
        const { config } = liveDefinition(ctx.modelRegistry, provider, definition);
        if (config) {
          pi.registerProvider(provider, config);
          await ctx.modelRegistry.refresh({ allowNetwork: false });
        }
        reply(ctx, id, { provider, model: definition.id });
      } catch (error) {
        reply(ctx, id, { error: error instanceof Error ? error.message : String(error) });
      }
    },
  });

  // Prepare an immutable route without changing the selection. Telos commits
  // it with ONE native set_model command. Neither hook ordering nor the mutable
  // session thinking level can split the pair seen by a provider request.
  pi.registerCommand("telos-internal-prepare-settings", {
    description: "Prepare one model and thinking configuration",
    handler: async (args, ctx) => {
      const request = JSON.parse(args);
      // Pi forwards extension status events over RPC and redirects direct
      // extension stdout to stderr. This private status carries the correlated
      // preparation result; it never becomes a model message or a user dialog.
      const respond = (data) => reply(ctx, request.id, data);
      try {
        const { provider, model: modelId, thinking, definition } = request;
        const prepared = definition ? liveDefinition(ctx.modelRegistry, provider, definition)
          : { model: ctx.modelRegistry.find(provider, modelId) };
        const overrides = pi.getSettings().compaction?.modelOverrides?.[provider + "/" + modelId];
        if (overrides?.reserveTokens !== undefined || overrides?.keepRecentTokens !== undefined) {
          return respond({ error: "Atomic settings changes cannot preserve this model's custom compaction settings" });
        }
        const { config } = prepared;
        let model = prepared.model ?? config?.models.find((candidate) => candidate.id === modelId);
        if (!model || model.api === "pi-virtual") {
          return respond({ error: `Physical model not found: ${provider}/${modelId}` });
        }
        const levels = getSupportedThinkingLevels(model);
        if (!levels.includes(thinking)) {
          return respond({ error: `Unsupported thinking level ${JSON.stringify(thinking)}; available: ${levels.join(", ")}` });
        }
        // Validate before refreshing metadata as well as before selection.
        // Pi re-resolves routes by provider/id on every request. Existing entries
        // must stay unchanged even when an older route has not started streaming.
        if (config) {
          pi.registerProvider(provider, config);
          await ctx.modelRegistry.refresh({ allowNetwork: false });
        }
        model = ctx.modelRegistry.getAvailable().find((candidate) => candidate.provider === provider && candidate.id === modelId);
        if (!model || model.api === "pi-virtual" || !getSupportedThinkingLevels(model).includes(thinking)) {
          return respond({ error: `Model and thinking configuration unavailable: ${provider}/${modelId}, ${thinking}` });
        }
        const id = "telos-internal-settings-" + randomUUID();
        const route = Object.freeze({ model, thinkingLevel: thinking });
        pi.registerVirtualModel({
          provider, id, name: model.name,
          thinkingLevels: [thinking], input: model.input,
          contextWindow: model.contextWindow, maxTokens: model.maxTokens,
          // Each registration is permanent for this invocation. Never replace
          // a route that a request may already have selected.
          route: () => route,
        });
        respond({ provider, model: modelId, thinking, modelId: id });
      } catch (error) {
        // This command only prepares a route; it never selects one.
        respond({ error: error instanceof Error ? error.message : String(error) });
      }
    },
  });
}
