import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { getBuiltinModels } from "@earendil-works/pi-ai/providers/all";

export default function (pi) {
  const file = process.env.TELOS_PI_MODEL_CONFIG;
  function register(currentModels = []) {
    const { provider, definition } = JSON.parse(readFileSync(file, "utf8"));
    if (!definition) return;
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
    for (const model of existing.models || []) {
      definitions.set(model.id, normalize(model));
    }
    for (const model of currentModels.filter((model) => model.provider === provider)) {
      definitions.set(model.id, model);
    }
    const config = { ...existing, api: definition.api ?? existing.api };
    if (provider === "openrouter" && (!existing.baseUrl || ["https://openrouter.ai/api", "https://openrouter.ai/api/v1"].includes(existing.baseUrl.replace(/\/$/, "")))) {
      config.baseUrl = config.api === "anthropic-messages" ? "https://openrouter.ai/api" : "https://openrouter.ai/api/v1";
      config.authHeader = existing.authHeader ?? true;
    }
    definitions.set(definition.id, normalize(definition, config.baseUrl));
    config.models = [...definitions.values()];
    pi.registerProvider(provider, config);
  }
  register();
  pi.registerCommand("telos-internal-refresh-model", {
    description: "Refresh the deployment's model definition",
    handler: async (_args, ctx) => {
      register(ctx.modelRegistry.getAll());
      await ctx.modelRegistry.refresh({ allowNetwork: false });
    },
  });
}
