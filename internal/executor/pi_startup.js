import { existsSync, readFileSync, writeSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

// Registers Telos's model definition for this Pi process.
export default async function (pi) {
  let model = "";
  try {
    const config = JSON.parse(readFileSync(process.env.TELOS_PI_STARTUP_CONFIG, "utf8"));
    model = config.model;
    const provider = model.slice(0, model.indexOf("/"));
    const { getBuiltinModels } = await import("@earendil-works/pi-ai/providers/all");
    pi.registerProvider(provider, providerConfig(provider, config.definition, getBuiltinModels(provider)));
  } catch (error) {
    // Pi swallows extension errors; exit so the turn fails before any prompt.
    // "invalid model" makes Telos wait for new settings instead of retrying.
    writeSync(2, "Telos could not register Pi model " + model + " (invalid model settings): " + (error instanceof Error ? error.message : String(error)) + "\n");
    process.exit(78);
  }
}

function providerConfig(provider, definition, builtins) {
  const modelsFile = join(process.env.PI_CODING_AGENT_DIR || join(homedir(), ".pi", "agent"), "models.json");
  const models = existsSync(modelsFile) ? JSON.parse(readFileSync(modelsFile, "utf8")) : {};
  const existing = models.providers?.[provider] || {};
  const definitions = new Map(builtins.map((model) => [
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
  const config = { ...existing, api: definition.api ?? existing.api };
  if (provider === "openrouter" && (!existing.baseUrl || ["https://openrouter.ai/api", "https://openrouter.ai/api/v1"].includes(existing.baseUrl.replace(/\/$/, "")))) {
    config.baseUrl = config.api === "anthropic-messages" ? "https://openrouter.ai/api" : "https://openrouter.ai/api/v1";
    config.authHeader = existing.authHeader ?? true;
  }
  definitions.set(definition.id, normalize(definition, config.baseUrl));
  config.models = [...definitions.values()];
  return config;
}
