// Test-only provider for exercising real Pi without a network server.
import { existsSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

export default async function (pi) {
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
      pid: process.pid, model: model.id, thinking: options?.reasoning ?? "off",
      api: model.api, contextWindow: model.contextWindow, maxTokens: model.maxTokens,
      messages: context.messages, headers: options?.headers,
      baseUrl: model.baseUrl, apiKey: options?.apiKey,
      proxy: options?.env?.HTTPS_PROXY, processProxy: process.env.HTTPS_PROXY,
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
          const environment = quote(join(directory, "tool-thinking"));
          const toolCall = {
            type: "toolCall", id: "call_probe", name: "bash",
            arguments: {
              command: "printf '%s\\n%s\\n' \"$TELOS_THINKING\" \"$TELOS_INHERITED_THINKING\" > " + environment +
                "; printf started > " + started + "; while [ ! -f " + released + " ]; do sleep 0.02; done; printf 'preserved tool result'",
            },
          };
          message.content.push(toolCall);
          stream.push({ type: "toolcall_start", contentIndex: 0, partial: message });
          stream.push({ type: "toolcall_end", contentIndex: 0, toolCall, partial: message });
          message.stopReason = "toolUse";
        } else {
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
  if (process.env.TELOS_PI_PROBE_PHASE?.startsWith("connection")) {
    const { registerApiProvider } = await import("@earendil-works/pi-ai/compat");
    registerApiProvider({ api: "telos-offline-test", stream: streamSimple, streamSimple });
  }
  for (const suffix of ["a", "b"]) {
    const provider = "turn-" + suffix;
    const config = {
      api: "telos-offline-test",
      apiKey: "test-only",
      baseUrl: "https://unused.invalid",
      models: [{
        id: "probe-" + suffix, name: "Offline test",
        reasoning: suffix !== "b" || process.env.TELOS_PI_PROBE_PHASE !== "model_only_nonreasoning",
        headers: { "x-telos-model-header": "preserved" },
        input: ["text"], contextWindow: 128000, maxTokens: 4096,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      }],
      streamSimple,
    };
    pi.registerProvider(provider, config);
  }
}
