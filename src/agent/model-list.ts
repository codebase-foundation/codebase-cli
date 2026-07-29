import type { Model } from "@earendil-works/pi-ai";

/**
 * Live model discovery for the /model picker and /models list. Works for
 * any session that talks to an endpoint with a model-list API: the
 * codebase proxy, OpenAI-compatible servers (OpenAI, Groq, OpenRouter,
 * Mistral, DeepSeek, xAI, and local Ollama / LM Studio / vLLM), plus
 * Anthropic and Google which use their own shapes. Returns [] (or throws)
 * when the endpoint can't be listed; callers degrade gracefully.
 */
export interface ModelOption {
	id: string;
	name: string;
	provider: string;
}

export async function fetchAvailableModels(
	model: Model<string>,
	apiKey: string | undefined,
	signal?: AbortSignal,
): Promise<ModelOption[]> {
	const provider = String(model.provider);
	if (provider === "anthropic") return fetchAnthropic(apiKey, signal);
	if (provider === "google") return fetchGoogle(apiKey, signal);
	const baseUrl = (model.baseUrl ?? "").replace(/\/+$/, "");
	if (!baseUrl) return [];
	return fetchOpenAiCompat(baseUrl, apiKey, provider, model.headers, signal);
}

/** OpenAI-compatible `GET {baseUrl}/models`. Also parses our proxy's `{models:[…]}` shape. */
async function fetchOpenAiCompat(
	baseUrl: string,
	apiKey: string | undefined,
	provider: string,
	modelHeaders: Record<string, string> | undefined,
	signal?: AbortSignal,
): Promise<ModelOption[]> {
	const headers: Record<string, string> = { ...modelHeaders, Accept: "application/json" };
	// Local servers (Ollama/LM Studio) usually need no auth; only send a key
	// when we have a real one. A model-level X-API-Key means this is a
	// manually entered Codebase key, so do not duplicate it as Bearer.
	const hasApiKeyHeader = Object.keys(headers).some((name) => name.toLowerCase() === "x-api-key");
	if (apiKey && !hasApiKeyHeader) headers.Authorization = `Bearer ${apiKey}`;
	const res = await fetch(`${baseUrl}/models`, { headers, signal });
	if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
	const json = (await res.json()) as { data?: unknown; models?: unknown };
	const raw = Array.isArray(json.data) ? json.data : Array.isArray(json.models) ? json.models : [];
	return normalize(raw, provider);
}

async function fetchAnthropic(apiKey: string | undefined, signal?: AbortSignal): Promise<ModelOption[]> {
	if (!apiKey) throw new Error("not signed in");
	const res = await fetch("https://api.anthropic.com/v1/models?limit=100", {
		headers: { "x-api-key": apiKey, "anthropic-version": "2023-06-01", Accept: "application/json" },
		signal,
	});
	if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
	const json = (await res.json()) as { data?: Array<{ id?: string; display_name?: string }> };
	return (json.data ?? [])
		.filter((m): m is { id: string; display_name?: string } => typeof m.id === "string")
		.map((m) => ({ id: m.id, name: m.display_name ?? m.id, provider: "anthropic" }));
}

async function fetchGoogle(apiKey: string | undefined, signal?: AbortSignal): Promise<ModelOption[]> {
	if (!apiKey) throw new Error("not signed in");
	const url = `https://generativelanguage.googleapis.com/v1beta/models?pageSize=200&key=${encodeURIComponent(apiKey)}`;
	const res = await fetch(url, { headers: { Accept: "application/json" }, signal });
	if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
	const json = (await res.json()) as {
		models?: Array<{ name?: string; displayName?: string; supportedGenerationMethods?: string[] }>;
	};
	return (json.models ?? [])
		.filter((m) => typeof m.name === "string")
		.filter((m) => !m.supportedGenerationMethods || m.supportedGenerationMethods.includes("generateContent"))
		.map((m) => {
			const id = (m.name as string).replace(/^models\//, "");
			return { id, name: m.displayName ?? id, provider: "google" };
		});
}

/** Map a raw model array (OpenAI `data` or proxy `models`) to ModelOptions. */
function normalize(raw: unknown[], fallbackProvider: string): ModelOption[] {
	const out: ModelOption[] = [];
	for (const item of raw) {
		if (!item || typeof item !== "object") continue;
		const m = item as { id?: unknown; name?: unknown; provider?: unknown };
		const id = typeof m.id === "string" ? m.id : typeof m.name === "string" ? m.name : "";
		if (!id) continue;
		const name = typeof m.name === "string" && m.name !== id ? m.name : id;
		const provider = typeof m.provider === "string" ? m.provider : fallbackProvider;
		out.push({ id, name, provider });
	}
	return out;
}
