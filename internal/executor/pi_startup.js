import { existsSync, readFileSync, renameSync, writeFileSync, writeSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { isDeepStrictEqual } from "node:util";

// This extension prepares and validates one turn. It accepts no live updates.
export default async function (pi) {
  let config;
  try {
    config = JSON.parse(readFileSync(process.env.TELOS_PI_STARTUP_CONFIG, "utf8"));
  } catch {
    process.exit(78);
  }
  let startupError;
  let getBuiltinModels;
  const report = (error, model, thinking) => {
    const receipt = {
      request_id: config.request_id, attempt_id: config.attempt_id,
      model, thinking, connection_id: config.connection?.id, error,
    };
    writeFileSync(config.receipt_path + ".tmp", JSON.stringify(receipt), { mode: 0o600 });
    renameSync(config.receipt_path + ".tmp", config.receipt_path);
    writeSync(1, "TELOS_PI_STARTUP " + config.attempt_id + "\n");
  };
  pi.on("session_start", (_event, ctx) => {
    try {
      if (startupError) throw startupError;
      const separator = config.model.indexOf("/");
      const provider = config.model.slice(0, separator);
      const id = config.model.slice(separator + 1);
      const model = ctx.modelRegistry.find(provider, id);
      // Pi can synthesize an unknown model and silently clamp thinking levels.
      if (!model || model.api === "pi-virtual") throw new Error("Model is not registered: " + config.model);
      if (ctx.model?.provider !== provider || ctx.model?.id !== id || pi.getThinkingLevel() !== config.thinking) {
        throw new Error("Pi did not accept the requested model and thinking level");
      }
      if (config.definition && Object.keys(config.definition).some((key) => !isDeepStrictEqual(config.definition[key], model[key]))) {
        throw new Error("Pi did not accept the requested model definition");
      }
      if (config.connection && (model.baseUrl !== config.connection.base_url || ctx.model?.baseUrl !== config.connection.base_url)) {
        throw new Error("Pi did not accept the requested inference connection");
      }
      report(undefined, config.model, config.thinking);
    } catch (error) {
      // Pi swallows extension errors. Even if the receipt cannot be written,
      // validation failure must terminate before the prompt is dispatched.
      try {
        report(error instanceof Error ? error.message : String(error));
      } finally {
        process.exit(78);
      }
    }
  });
  function providerConfig(provider, definition) {
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
    const config = { ...existing, api: definition?.api ?? existing.api };
    if (provider === "openrouter" && (!existing.baseUrl || ["https://openrouter.ai/api", "https://openrouter.ai/api/v1"].includes(existing.baseUrl.replace(/\/$/, "")))) {
      config.baseUrl = config.api === "anthropic-messages" ? "https://openrouter.ai/api" : "https://openrouter.ai/api/v1";
      config.authHeader = existing.authHeader ?? true;
    }
    if (definition) definitions.set(definition.id, normalize(definition, config.baseUrl));
    config.models = [...definitions.values()];
    return config;
  }
  if (config.connection || config.definition) {
    try {
      ({ getBuiltinModels } = await import("@earendil-works/pi-ai/providers/all"));
      const provider = config.model.slice(0, config.model.indexOf("/"));
      if (config.connection) {
        const { VERSION } = await import("@earendil-works/pi-coding-agent");
        const [major, minor, patch] = VERSION.split(".").map(Number);
        if (typeof Bun === "undefined" || major !== 1 || !(minor > 0 || (minor === 0 && patch >= 4))) {
          throw new Error("Inference connection switching requires the compiled Pi 1.0.4 or newer 1.x runtime");
        }
        const { getApiProvider } = await import("@earendil-works/pi-ai/compat");
        const id = config.model.slice(config.model.indexOf("/") + 1);
        const definition = providerConfig(provider, config.definition).models.find((model) => model.id === id);
        if (!definition) throw new Error("Model is not registered for the inference connection: " + config.model);
        const adapter = getApiProvider(definition.api);
        if (!adapter) throw new Error("Unsupported inference API: " + definition.api);
        const nativeFetch = globalThis.fetch.bind(globalThis);
        const proxy = config.connection.proxy_url;
        const headers = {
          // The OpenAI SDK otherwise restores account routing from process.env.
          ...(["openai-completions", "openai-responses"].includes(definition.api) ? {
            "OpenAI-Organization": null,
            "OpenAI-Project": null,
          } : {}),
          ...(config.connection.auth_header ? { Authorization: `Bearer ${config.connection.api_key}` } : {}),
          ...(provider === "telos-bifrost" ? { "x-bf-vk": config.connection.api_key } : {}),
        };
        // This registration belongs to this invocation. Replacing an account
        // must not inherit the previous provider's auth, headers, or endpoint.
        pi.registerProvider(provider, {
          api: definition.api,
          apiKey: config.connection.api_key,
          baseUrl: config.connection.base_url,
          authHeader: config.connection.auth_header || false,
          models: [{ ...definition, headers: undefined, baseUrl: config.connection.base_url }],
          streamSimple: (model, context, options) => adapter.streamSimple({
            ...model,
            provider,
            api: definition.api,
            baseUrl: config.connection.base_url,
            headers: undefined,
          }, context, {
            ...options,
            apiKey: config.connection.api_key,
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
        });
      } else {
        pi.registerProvider(provider, providerConfig(provider, config.definition));
      }
    } catch (error) {
      startupError = error;
    }
  }
}
