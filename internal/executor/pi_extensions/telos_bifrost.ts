import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { convertToLlm, serializeConversation } from "@earendil-works/pi-coding-agent";

type Headers = Record<string, string>;

const providerName = "telos-bifrost";
const routingEntryType = "telos-bifrost-routing";

function env(name: string): string {
  return (process.env[name] ?? "").trim();
}

function parseHeaders(): Headers {
  const raw = env("TELOS_GATEWAY_HEADERS");
  if (!raw) return {};
  const parsed = JSON.parse(raw) as Record<string, unknown>;
  const headers: Headers = {};
  for (const [key, value] of Object.entries(parsed)) {
    if (key.trim() && typeof value === "string") {
      headers[key.trim()] = value.trim();
    }
  }
  return headers;
}

function headerValue(headers: Record<string, unknown>, name: string): string {
  const wanted = name.toLowerCase();
  for (const [key, value] of Object.entries(headers ?? {})) {
    if (key.toLowerCase() === wanted && value != null) {
      return String(value).trim();
    }
  }
  return "";
}

function profile(): "standard" | "premium" {
  return env("TELOS_MODEL_PROFILE") === "premium" ? "premium" : "standard";
}

function staticHeaders(): Headers {
  return parseHeaders();
}

function requestID(suffix = ""): string {
  const id = env("TELOS_BIFROST_REQUEST_ID");
  if (!id) return "";
  return suffix ? `${id}:${suffix}` : id;
}

function agentHeaders(): Headers {
  const assignedProvider = env("TELOS_BIFROST_ASSIGNED_PROVIDER") || "unset";
  const phase = assignedProvider === "unset" ? "new" : env("TELOS_BIFROST_AGENT_PHASE") || "existing";
  return {
    "x-bf-session-id": env("TELOS_BIFROST_AGENT_SESSION_ID"),
    "x-bf-session-ttl": env("TELOS_BIFROST_SESSION_TTL") || "1h",
    "x-bf-cache-key": env("TELOS_BIFROST_AGENT_CACHE_KEY"),
    "x-llm-usecase": "agent",
    "x-llm-session-phase": phase,
    "x-llm-assigned-provider": assignedProvider,
    "x-telos-model-profile": profile(),
    "x-request-id": requestID(),
  };
}

function compactionHeaders(): Headers {
  return {
    "x-bf-session-id": env("TELOS_BIFROST_COMPACTION_SESSION_ID"),
    "x-bf-session-ttl": env("TELOS_BIFROST_SESSION_TTL") || "1h",
    "x-bf-cache-key": env("TELOS_BIFROST_COMPACTION_CACHE_KEY"),
    "x-llm-usecase": "compaction",
    "x-llm-session-phase": "existing",
    "x-llm-assigned-provider": profile() === "standard" ? "silares" : env("TELOS_BIFROST_ASSIGNED_PROVIDER") || "unset",
    "x-telos-model-profile": profile(),
    "x-request-id": requestID("compaction"),
  };
}

function model(id: string, name: string, contextWindow: number, headers: Headers, zaiThinking: boolean) {
  const compat: Record<string, unknown> = {
    supportsReasoningEffort: true,
    maxTokensField: "max_tokens",
    cacheControlFormat: "anthropic",
  };
  if (zaiThinking) {
    compat.thinkingFormat = "zai";
  }
  return {
    id,
    name,
    reasoning: true,
    thinkingLevelMap: { minimal: null, low: null, medium: "medium", high: "high", xhigh: "max" },
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow,
    maxTokens: 65536,
    headers,
    compat,
  };
}

function appendRouting(pi: ExtensionAPI, headers: Record<string, unknown>, fallbackModel = "", ok = true) {
  const provider = headerValue(headers, "x-telos-routed-provider");
  const routedModel = headerValue(headers, "x-telos-routed-model") || fallbackModel;
  if (!provider && !routedModel) return;
  const fallback = headerValue(headers, "x-telos-routing-fallback").toLowerCase() === "true";
  pi.appendEntry(routingEntryType, {
    provider,
    model: routedModel,
    fallback,
    ok,
  });
}

async function runCompaction(pi: ExtensionAPI, event: any) {
  const baseURL = env("TELOS_GATEWAY_BASE_URL").replace(/\/+$/, "");
  const apiKey = env("TELOS_GATEWAY_API_KEY");
  if (!baseURL || !apiKey) return undefined;

  const preparation = event.preparation;
  const conversation = serializeConversation(convertToLlm(preparation.messagesToSummarize));
  const prefixConversation = preparation.turnPrefixMessages?.length
    ? serializeConversation(convertToLlm(preparation.turnPrefixMessages))
    : "";
  const previous = preparation.previousSummary ? `\n\nPrevious summary:\n${preparation.previousSummary}` : "";
  const instructions = event.customInstructions ? `\n\nFocus instructions:\n${event.customInstructions}` : "";
  const prefix = prefixConversation ? `\n\nActive turn context to preserve:\n${prefixConversation}` : "";
  const prompt = [
    "Summarize the conversation for continuation. Preserve goals, decisions, constraints, file changes, open problems, and concrete next steps.",
    previous,
    instructions,
    prefix,
    "\n\nConversation to summarize:\n",
    conversation,
  ].join("");
  const modelName = `${profile()}-compaction`;
  const response = await fetch(`${baseURL}/chat/completions`, {
    method: "POST",
    headers: {
      ...staticHeaders(),
      ...compactionHeaders(),
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      model: modelName,
      messages: [
        { role: "system", content: "You write compact, factual continuation summaries for coding-agent sessions." },
        { role: "user", content: prompt },
      ],
      max_tokens: 4096,
      stream: false,
    }),
    signal: event.signal,
  });
  appendRouting(pi, Object.fromEntries(response.headers.entries()), modelName, response.ok);
  if (!response.ok) {
    throw new Error(`Telos Bifrost compaction failed: HTTP ${response.status}: ${await response.text()}`);
  }
  const body = (await response.json()) as any;
  const summary = body?.choices?.[0]?.message?.content ?? body?.output_text ?? "";
  if (!summary.trim()) {
    throw new Error("Telos Bifrost compaction returned no summary");
  }
  return {
    compaction: {
      summary,
      firstKeptEntryId: preparation.firstKeptEntryId,
      tokensBefore: preparation.tokensBefore,
      details: { provider: providerName, model: modelName },
    },
  };
}

export default function (pi: ExtensionAPI) {
  const baseURL = env("TELOS_GATEWAY_BASE_URL");
  const apiKey = env("TELOS_GATEWAY_API_KEY");
  if (!baseURL || !apiKey) return;

  pi.registerProvider(providerName, {
    name: "Telos Bifrost",
    baseUrl: baseURL,
    apiKey,
    authHeader: true,
    api: "openai-completions",
    headers: staticHeaders(),
    models: [
      model("standard-agent", "Telos Standard Agent", 638000, agentHeaders(), true),
      model("standard-compaction", "Telos Standard Compaction", 1000000, compactionHeaders(), true),
      model("premium-agent", "Telos Premium Agent", 1000000, agentHeaders(), false),
      model("premium-compaction", "Telos Premium Compaction", 1000000, compactionHeaders(), false),
    ],
  });

  pi.on("after_provider_response", (event) => {
    const status = Number(event.status ?? event.statusCode ?? 200);
    appendRouting(pi, event.headers ?? {}, "", status >= 200 && status < 300);
  });

  pi.on("session_before_compact", async (event) => runCompaction(pi, event));
}
