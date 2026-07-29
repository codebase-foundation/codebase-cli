import type { AgentTool } from "@earendil-works/pi-agent-core";
import { type Static, Type } from "typebox";
import type { ToolContext } from "./types.js";

const Params = Type.Object({
	query: Type.String({
		minLength: 1,
		maxLength: 500,
		description: "Search query.",
	}),
	max_results: Type.Optional(
		Type.Integer({
			minimum: 1,
			maximum: 20,
			description: "Number of results to return. Default 5, max 20.",
		}),
	),
	timeout_ms: Type.Optional(
		Type.Integer({
			minimum: 500,
			maximum: 60_000,
			description: "Abort the search after this many ms. Default 15000.",
		}),
	),
});

export type WebSearchParams = Static<typeof Params>;

export interface SearchResult {
	title: string;
	url: string;
	snippet: string;
}

export interface WebSearchDetails {
	query: string;
	provider: ProviderName;
	results: SearchResult[];
	durationMs: number;
}

const DEFAULT_RESULTS = 5;
const DEFAULT_TIMEOUT_MS = 15_000;

const DESCRIPTION = `Search the web. Returns title + url + snippet for each result, formatted so the model can pick a URL and pass it to web_fetch.

Provider auto-detected from env (highest-priority match wins):
- TAVILY_API_KEY  → Tavily (recommended; free tier available)
- BRAVE_API_KEY   → Brave Search
- SEARXNG_URL     → self-hosted SearXNG instance
- Codebase OAuth  → hosted search (when signed in)

Local providers take priority over hosted search. If none are available, this tool errors with onboarding instructions instead of attempting an unauthenticated fallback. Default 5 results, max 20.`;

export type ProviderName = "tavily" | "brave" | "searxng" | "codebase";

interface ProviderConfig {
	name: ProviderName;
	search: (query: string, count: number, signal: AbortSignal) => Promise<SearchResult[]>;
}

export interface ProviderEnv {
	TAVILY_API_KEY?: string;
	TAVILY_BASE_URL?: string;
	BRAVE_API_KEY?: string;
	BRAVE_BASE_URL?: string;
	SEARXNG_URL?: string;
}

export interface HostedSearchConfig {
	baseUrl: string;
	getAccessToken: () => Promise<string>;
}

export function createWebSearch(ctx: ToolContext): AgentTool<typeof Params, WebSearchDetails> {
	return {
		name: "web_search",
		label: "Search",
		description: DESCRIPTION,
		parameters: Params,
		executionMode: "parallel",
		execute: async (_id, params, signal) => {
			const provider = pickProvider(process.env, ctx.platform);
			const max = params.max_results ?? DEFAULT_RESULTS;
			const timeout = params.timeout_ms ?? DEFAULT_TIMEOUT_MS;

			const controller = new AbortController();
			const onUpstreamAbort = () => controller.abort();
			signal?.addEventListener("abort", onUpstreamAbort);
			const timer = setTimeout(() => controller.abort(new Error("timeout")), timeout);

			const startedAt = Date.now();
			let results: SearchResult[];
			try {
				results = await provider.search(params.query, max, controller.signal);
			} catch (err) {
				const reason = err instanceof Error ? err.message : String(err);
				if (controller.signal.aborted && !signal?.aborted) {
					throw new Error(`web_search timed out after ${Math.round(timeout / 1000)}s.`);
				}
				throw new Error(`web_search (${provider.name}) failed: ${reason}`);
			} finally {
				clearTimeout(timer);
				signal?.removeEventListener("abort", onUpstreamAbort);
			}

			const durationMs = Date.now() - startedAt;
			return {
				content: [{ type: "text", text: formatResults(provider.name, params.query, results) }],
				details: { query: params.query, provider: provider.name, results, durationMs },
			};
		},
	};
}

export function pickProvider(env: ProviderEnv, hosted?: HostedSearchConfig): ProviderConfig {
	if (env.TAVILY_API_KEY) {
		return tavilyProvider(env.TAVILY_API_KEY, env.TAVILY_BASE_URL);
	}
	if (env.BRAVE_API_KEY) {
		return braveProvider(env.BRAVE_API_KEY, env.BRAVE_BASE_URL);
	}
	if (env.SEARXNG_URL) {
		return searxngProvider(env.SEARXNG_URL);
	}
	if (hosted) {
		return codebaseProvider(hosted);
	}
	throw new Error(
		"SEARCH_FAILED_NO_RESULTS: web_search has no provider configured. Sign in with `codebase auth login`, " +
			"or set TAVILY_API_KEY, BRAVE_API_KEY, or SEARXNG_URL. No web results were obtained; do not invent " +
			"or present search results.",
	);
}

function codebaseProvider(config: HostedSearchConfig): ProviderConfig {
	const url = `${config.baseUrl.replace(/\/+$/, "")}/api/v1/search`;
	return {
		name: "codebase",
		search: async (query, max, signal) => {
			const token = await config.getAccessToken();
			const res = await fetch(url, {
				method: "POST",
				headers: {
					Authorization: `Bearer ${token}`,
					"Content-Type": "application/json",
					Accept: "application/json",
				},
				body: JSON.stringify({ query, max_results: max }),
				signal,
			});
			const json = (await res.json().catch(() => ({}))) as {
				error?: string;
				error_description?: string;
				results?: SearchResult[];
			};
			if (!res.ok) {
				const reason = json.error_description ?? json.error ?? res.statusText;
				throw new Error(`codebase ${res.status} ${reason}`);
			}
			return Array.isArray(json.results)
				? json.results.filter(
						(result) =>
							typeof result?.title === "string" &&
							typeof result?.url === "string" &&
							typeof result?.snippet === "string",
					)
				: [];
		},
	};
}

function tavilyProvider(apiKey: string, baseUrl?: string): ProviderConfig {
	const url = `${baseUrl ?? "https://api.tavily.com"}/search`;
	return {
		name: "tavily",
		search: async (query, max, signal) => {
			const res = await fetch(url, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({
					api_key: apiKey,
					query,
					max_results: max,
					search_depth: "basic",
					include_answer: false,
				}),
				signal,
			});
			if (!res.ok) throw new Error(`tavily ${res.status} ${res.statusText}`);
			const json = (await res.json()) as { results?: { title: string; url: string; content: string }[] };
			return (json.results ?? []).map((r) => ({
				title: r.title,
				url: r.url,
				snippet: r.content,
			}));
		},
	};
}

function braveProvider(apiKey: string, baseUrl?: string): ProviderConfig {
	const url = baseUrl ?? "https://api.search.brave.com/res/v1/web/search";
	return {
		name: "brave",
		search: async (query, max, signal) => {
			const u = new URL(url);
			u.searchParams.set("q", query);
			u.searchParams.set("count", String(max));
			const res = await fetch(u, {
				headers: { "X-Subscription-Token": apiKey, Accept: "application/json" },
				signal,
			});
			if (!res.ok) throw new Error(`brave ${res.status} ${res.statusText}`);
			const json = (await res.json()) as {
				web?: { results?: { title: string; url: string; description: string }[] };
			};
			return (json.web?.results ?? []).map((r) => ({
				title: r.title,
				url: r.url,
				snippet: r.description,
			}));
		},
	};
}

function searxngProvider(baseUrl: string): ProviderConfig {
	const root = baseUrl.replace(/\/+$/, "");
	return {
		name: "searxng",
		search: async (query, max, signal) => {
			const u = new URL(`${root}/search`);
			u.searchParams.set("q", query);
			u.searchParams.set("format", "json");
			const res = await fetch(u, { signal });
			if (!res.ok) throw new Error(`searxng ${res.status} ${res.statusText}`);
			const json = (await res.json()) as { results?: { title: string; url: string; content?: string }[] };
			return (json.results ?? []).slice(0, max).map((r) => ({
				title: r.title,
				url: r.url,
				snippet: r.content ?? "",
			}));
		},
	};
}

function formatResults(provider: string, query: string, results: SearchResult[]): string {
	if (results.length === 0) {
		return `No results for "${query}" via ${provider}.`;
	}
	const lines: string[] = [`${results.length} results from ${provider} for "${query}":`, ""];
	for (let i = 0; i < results.length; i++) {
		const r = results[i];
		lines.push(`${i + 1}. ${r.title}`);
		lines.push(`   ${r.url}`);
		if (r.snippet) {
			const trimmed = r.snippet.length > 300 ? `${r.snippet.slice(0, 300)}…` : r.snippet;
			lines.push(`   ${trimmed}`);
		}
		lines.push("");
	}
	return lines.join("\n").trimEnd();
}
