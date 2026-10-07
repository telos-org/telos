// Test-only provider for exercising real Pi without a network server.
import { existsSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

export default function (pi) {
  const directory = process.env.TELOS_PI_PROBE_DIR;
  let requestCount = 0;
  const record = (name, value) => {
    const path = join(directory, name);
    writeFileSync(path + ".tmp", JSON.stringify(value));
    renameSync(path + ".tmp", path);
  };
  const quote = (value) => "'" + value.replaceAll("'", "'\\''") + "'";
  const waitForRelease = async (signal, file = "request-release") => {
    while (!existsSync(join(directory, file))) {
      if (signal?.aborted) {
        throw new Error("Request aborted");
      }
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  };
  // Loaded before Telos's extension: a later model_select callback cannot fix
  // a split pair while this handler yields to the next provider request.
  pi.on("model_select", async (event, ctx) => {
    if (process.env.TELOS_PI_PROBE_PHASE === "atomic_boundary" && event.source === "set") {
      record("model-select-started", {});
      await waitForRelease(ctx.signal, "model-select-release");
    }
  });
  const streamSimple = (model, context, options) => {
    const stream = createAssistantMessageEventStream();
    const number = ++requestCount;
    const message = {
      role: "assistant",
      provider: model.provider,
      model: model.id,
      api: model.api,
      content: [],
      stopReason: "pending",
      timestamp: Date.now(),
      usage: {
        input: 10, output: 5, cacheRead: 0, cacheWrite: 0, totalTokens: 15,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
      },
    };
    record("request-" + number + ".json", {
      pid: process.pid, model: model.id, thinking: options?.reasoning,
      messages: context.messages, headers: options?.headers,
    });
    (async () => {
      try {
        stream.push({ type: "start", partial: message });
        if (number === 1) {
          if (process.env.TELOS_PI_PROBE_PHASE !== "tool") {
            await waitForRelease(options?.signal);
          }
          const started = quote(join(directory, "tool-started"));
          const released = quote(join(directory, "tool-release"));
          const toolCall = {
            type: "toolCall", id: "call_probe", name: "bash",
            arguments: { command: "printf started > " + started + "; while [ ! -f " + released + " ]; do sleep 0.02; done; printf 'preserved tool result'" },
          };
          message.content.push(toolCall);
          stream.push({ type: "toolcall_start", contentIndex: 0, partial: message });
          stream.push({ type: "toolcall_end", contentIndex: 0, toolCall, partial: message });
          message.stopReason = "toolUse";
        } else {
          if (process.env.TELOS_PI_PROBE_PHASE === "atomic_boundary") {
            await waitForRelease(options?.signal, "second-response-release");
          }
          const content = "Complete.\n<status>CONCEDE</status>";
          message.content.push({ type: "text", text: content });
          stream.push({ type: "text_start", contentIndex: 0, partial: message });
          stream.push({ type: "text_delta", contentIndex: 0, delta: content, partial: message });
          stream.push({ type: "text_end", contentIndex: 0, content, partial: message });
          message.stopReason = "stop";
        }
        stream.push({ type: "done", reason: message.stopReason, message });
      } catch (error) {
        message.stopReason = "aborted";
        message.errorMessage = String(error);
        stream.push({ type: "error", reason: "aborted", error: message });
      } finally {
        stream.end();
      }
    })();
    return stream;
  };
  for (const suffix of ["a", "b"]) {
    const provider = "rpc-" + suffix;
    const config = {
      api: "telos-offline-test",
      apiKey: "test-only",
      baseUrl: "https://unused.invalid",
      models: [{
        id: "probe-" + suffix, name: "Offline test", reasoning: true,
        headers: { "x-telos-model-header": "preserved" },
        input: ["text"], contextWindow: 128000, maxTokens: 4096,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      }],
      streamSimple,
    };
    if (process.env.TELOS_PI_PROBE_PHASE?.includes("native")) {
      pi.registerProvider({
        id: provider, name: "Native offline provider", baseUrl: config.baseUrl,
        auth: { apiKey: { name: "Test key", resolve: async () => ({ auth: { apiKey: "test-only" } }) } },
        getModels: () => config.models.map((model) => ({ ...model, provider, api: config.api, baseUrl: config.baseUrl })),
        stream: streamSimple, streamSimple,
      });
    } else {
      pi.registerProvider(provider, config);
    }
  }
}
