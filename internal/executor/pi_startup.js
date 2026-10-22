import { existsSync, readFileSync, writeSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

// Registers Telos's model definition and connection for this Pi process.
export default async function (pi) {
  let model = "";
  try {
    const config = JSON.parse(readFileSync(process.env.TELOS_PI_STARTUP_CONFIG, "utf8"));
    model = config.model;
    const provider = model.slice(0, model.indexOf("/"));
    const { getBuiltinModels } = await import("@earendil-works/pi-ai/providers/all");
    const registered = providerConfig(provider, config.definition, getBuiltinModels(provider));
    if (config.connection) {
      const definition = registered.models.find((entry) => entry.id === model.slice(provider.length + 1));
      if (!definition) throw new Error("model is not registered for the inference connection");
      pi.registerProvider(provider, await connectionConfig(provider, definition, config.connection));
    } else {
      pi.registerProvider(provider, registered);
    }
  } catch (error) {
    // Pi swallows extension errors; exit so the turn fails before any prompt.
    writeSync(2, "Telos could not register Pi model " + model + ": " + (error instanceof Error ? error.message : String(error)) + "\n");
    process.exit(78);
  }
}

async function connectionConfig(provider, definition, connection) {
  const { VERSION } = await import("@earendil-works/pi-coding-agent");
  const [major, minor, patch] = VERSION.split(".").map(Number);
  if (typeof Bun === "undefined" || major !== 1 || !(minor > 0 || (minor === 0 && patch >= 4))) {
    throw new Error("inference connections require the compiled Pi 1.0.4 or newer 1.x runtime; found " + VERSION);
  }
  const { getApiProvider } = await import("@earendil-works/pi-ai/compat");
  const adapter = getApiProvider(definition.api);
  if (!adapter) throw new Error("unsupported inference API: " + definition.api);
  const nativeFetch = globalThis.fetch.bind(globalThis);
  const proxy = connection.proxy_url;
  const headers = {
    // The OpenAI SDK otherwise restores account routing from process.env.
    ...(["openai-completions", "openai-responses"].includes(definition.api) ? {
      "OpenAI-Organization": null,
      "OpenAI-Project": null,
    } : {}),
    ...(connection.auth_header ? { Authorization: `Bearer ${connection.api_key}` } : {}),
    ...(provider === "telos-bifrost" ? { "x-bf-vk": connection.api_key } : {}),
  };
  // Replacing an account must not inherit the previous provider's auth,
  // headers, or endpoint from models.json.
  return {
    api: definition.api,
    apiKey: connection.api_key,
    baseUrl: connection.base_url,
    authHeader: connection.auth_header || false,
    models: [{ ...definition, headers: undefined, baseUrl: connection.base_url }],
    streamSimple: (model, context, options) => adapter.streamSimple({
      ...model,
      provider,
      api: definition.api,
      baseUrl: connection.base_url,
      headers: undefined,
    }, context, {
      ...options,
      apiKey: connection.api_key,
      // Pi composes models.json and stored-auth headers after registration.
      // Only this connection's headers may reach its native adapter.
      headers,
      fetch: (input, init) => nativeFetch(input, { ...init, proxy }),
      // Codex WebSocket requests read provider-scoped proxy variables.
      // Tool subprocesses retain the ordinary process environment.
      env: {
        ...options?.env,
        HTTP_PROXY: proxy, HTTPS_PROXY: proxy, ALL_PROXY: proxy,
        http_proxy: proxy, https_proxy: proxy, all_proxy: proxy,
        OPENCLAW_PROXY_URL: proxy, NO_PROXY: "", no_proxy: "",
      },
    }),
  };
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
  const config = { ...existing, api: definition?.api ?? existing.api };
  if (provider === "openrouter" && (!existing.baseUrl || ["https://openrouter.ai/api", "https://openrouter.ai/api/v1"].includes(existing.baseUrl.replace(/\/$/, "")))) {
    config.baseUrl = config.api === "anthropic-messages" ? "https://openrouter.ai/api" : "https://openrouter.ai/api/v1";
    config.authHeader = existing.authHeader ?? true;
  }
  if (definition) definitions.set(definition.id, normalize(definition, config.baseUrl));
  config.models = [...definitions.values()];
  return config;
}
