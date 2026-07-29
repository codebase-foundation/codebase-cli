import { getEnvApiKey, getModel, type KnownProvider, type Model } from "@earendil-works/pi-ai";
import { CredentialsStore } from "../auth/credentials.js";

/**
 * Provider+model selection, in priority order:
 *   1. Saved OAuth/API credentials (~/.codebase/credentials.json) — if
 *      present and not expired, route the chosen model through the
 *      codebase.design inference proxy so the backend can deduct
 *      credits. The model metadata stays the same; only baseUrl + apiKey
 *      change.
 *   2. CODEBASE_PROVIDER + CODEBASE_MODEL env (explicit override)
 *   3. First provider in {@link AUTO_DETECT_ORDER} with a usable env key
 *   4. Throw — caller should surface this as an onboarding hint
 *
 * pi-ai's `getEnvApiKey` already handles every quirky case: OAuth-only
 * providers, Vertex ADC, AWS multi-source. We just trust it.
 */

// The web's inference proxy lives under /api/inference/* — see
// docs/oauth-web-alignment-2026-05-08.md. pi-ai's per-protocol path
// appending lands the request on /api/inference/v1/messages
// (Anthropic) or /api/inference/chat (OpenAI-compat) once baseUrl
// points here.
const DEFAULT_PROXY_BASE = "https://codebase.design/api/inference";

const AUTO_DETECT_ORDER: readonly KnownProvider[] = [
	"anthropic",
	"openai",
	"groq",
	"google",
	"mistral",
	"deepseek",
	"cerebras",
	"xai",
	"openrouter",
] as const;

const DEFAULT_MODELS: Partial<Record<KnownProvider, string>> = {
	anthropic: "claude-sonnet-4-6",
	openai: "gpt-5.1",
	groq: "llama-3.3-70b-versatile",
	google: "gemini-2.5-pro",
	mistral: "mistral-large-latest",
	deepseek: "deepseek-chat",
	cerebras: "llama-3.3-70b",
	xai: "grok-4",
	openrouter: "anthropic/claude-sonnet-4",
};

export interface ResolvedConfig {
	model: Model<string>;
	apiKey: string;
	source: "explicit" | "auto" | "proxy" | "byok";
	/** Header contract for Codebase proxy credentials. OAuth uses Bearer; issued keys use X-API-Key. */
	proxyAuth?: "bearer" | "api-key";
}

export class ConfigError extends Error {}

export interface ResolveConfigOptions {
	env?: NodeJS.ProcessEnv;
	credentials?: CredentialsStore;
	/**
	 * Runtime model override. When set on an OAuth/proxy session, replaces
	 * the resolved model id (and optionally provider) so a user can swap
	 * mid-session via `/model <id>` without restarting. Ignored when no
	 * proxy session is active.
	 */
	modelOverride?: { provider?: string; modelId: string };
}

export function resolveConfig(envOrOpts: NodeJS.ProcessEnv | ResolveConfigOptions = process.env): ResolvedConfig {
	const opts = isProcessEnv(envOrOpts) ? { env: envOrOpts } : envOrOpts;
	const env = opts.env ?? process.env;
	const credentials = opts.credentials ?? new CredentialsStore();
	const override = opts.modelOverride;

	// 1. Saved credentials. Routing depends on the source:
	//    - codebase / manual → proxy through codebase.design
	//    - byok               → direct call against the provider's own API
	const useProxy = env.CODEBASE_DISABLE_PROXY !== "1";
	const creds = credentials.load();
	// Refreshable OAuth credentials remain sufficient to construct a proxy
	// agent even when the current access token is wire-dead. The agent's
	// TokenManager refreshes before the first provider request. Rejecting the
	// credentials here creates a chicken-and-egg for long-lived ACP servers:
	// they cannot create the next session that would perform the refresh.
	const usableCredentials =
		creds && (!credentials.isExpired(creds) || (creds.source === "codebase" && Boolean(creds.refreshToken)));
	if (usableCredentials) {
		if (creds.source === "byok" && creds.provider === "openai-compat" && creds.baseUrl) {
			// Custom Chat Completions endpoint saved by the wizard (Ollama,
			// LM Studio, vLLM, …). Same synthesis as the OPENAI_BASE_URL env
			// path, but persisted so it survives restarts. A /model override
			// swaps the id against the same local endpoint + key.
			const compat = buildOpenAiCompatConfig({
				baseUrl: creds.baseUrl,
				modelId: override?.modelId ?? creds.model ?? "default",
				apiKey: creds.accessToken,
				contextWindow: creds.contextWindow,
			});
			if (compat) return { ...compat, source: "byok" };
		} else if (creds.source === "byok" && creds.provider) {
			const byok = buildByokConfig(creds.provider as KnownProvider, creds.accessToken, override);
			if (byok) return byok;
		} else if (useProxy) {
			const proxied = buildProxiedConfig(
				env,
				creds.accessToken,
				creds.source === "manual" ? "api-key" : "bearer",
				override,
			);
			if (proxied) return proxied;
		}
	}

	// 2. OpenAI-compatible custom endpoint. Triggers when the user has
	//    set OPENAI_BASE_URL alongside an api key — that combination
	//    only makes sense for a custom Chat Completions backend (Groq,
	//    Ollama, MiniMax in-house, Qwen in-house, vLLM, …). pi-ai's
	//    registry only knows about a fixed set of providers/models, so
	//    we synthesize a Model object on the fly using a known
	//    chat-completions template and overriding id + baseUrl.
	if (env.OPENAI_BASE_URL && env.OPENAI_API_KEY && env.OPENAI_MODEL) {
		const compat = buildOpenAiCompatConfig({
			baseUrl: env.OPENAI_BASE_URL,
			modelId: override?.modelId ?? env.OPENAI_MODEL,
			apiKey: env.OPENAI_API_KEY,
		});
		if (compat) return compat;
	}

	const explicitProvider = env.CODEBASE_PROVIDER as KnownProvider | undefined;
	const explicitModel = env.CODEBASE_MODEL;

	if (explicitProvider && explicitModel) {
		// A /model override swaps the id at runtime; the launch env value is
		// the default. An id pi-ai doesn't know is id-cloned from the launch
		// model so any model the provider lists is switchable (only the
		// initial launch id, with no override, must be registry-known).
		const wantId = override?.modelId ?? explicitModel;
		let model = getModel(explicitProvider, wantId as never) as Model<string> | undefined;
		if (!model && override?.modelId) {
			const base = getModel(explicitProvider, explicitModel as never) as Model<string> | undefined;
			if (base) model = { ...base, id: wantId, name: wantId };
		}
		if (!model) {
			throw new ConfigError(
				`CODEBASE_PROVIDER=${explicitProvider} CODEBASE_MODEL=${explicitModel} not in pi-ai's model registry. ` +
					`Check the spelling, or unset both to auto-detect.`,
			);
		}
		const apiKey = getEnvApiKey(explicitProvider);
		if (!apiKey) {
			throw new ConfigError(
				`CODEBASE_PROVIDER=${explicitProvider} has no API key in env. Set the appropriate *_API_KEY var.`,
			);
		}
		return { model, apiKey, source: "explicit" };
	}

	// Env-var auto-detect is the legacy power-user path. We only honor it
	// for users who have already onboarded — otherwise a stray
	// ANTHROPIC_API_KEY or OPENAI_API_KEY in the shell would silently
	// skip the OAuth wizard the first time a new user runs `codebase`,
	// even though they almost certainly want the in-house default model
	// with free credits rather than spending their own API key budget.
	//
	// "Already onboarded" = credentials.json exists on disk, even if it's
	// now expired or empty. The wizard creates that file on first
	// sign-in, so its presence is a reliable "this user knows what
	// they're doing" signal.
	const hasOnboarded = credentials.exists();
	if (hasOnboarded) {
		for (const provider of AUTO_DETECT_ORDER) {
			const apiKey = getEnvApiKey(provider);
			if (!apiKey) continue;

			const modelId = DEFAULT_MODELS[provider];
			if (!modelId) continue;

			const model = getModel(provider, modelId as never);
			if (!model) continue;

			return { model, apiKey, source: "auto" };
		}
	}

	throw new ConfigError(
		"No LLM provider configured. Run `codebase auth login` for Codebase Auto, `codebase --new` for BYOK setup, " +
			"or set an *_API_KEY env var. Advanced users can also set CODEBASE_PROVIDER + CODEBASE_MODEL.",
	);
}

/**
 * Build a model that routes through codebase.design's inference proxy.
 *
 * Default when signed in via OAuth: "Codebase Auto" — the in-house
 * DeepSeek V4 Flash (`d4f`) served via the OpenAI-compatible protocol.
 * This matches the Codebase provider entry in
 * web/backend/providers/registry.js.
 *
 * Override via env: CODEBASE_PROVIDER + CODEBASE_MODEL still pick a
 * specific upstream from pi-ai's registry, also routed through the
 * proxy. The proxy dispatches by the model id in the request body +
 * the bearer scope on the token, so this works for any model the
 * user's account has access to.
 */
function buildProxiedConfig(
	env: NodeJS.ProcessEnv,
	accessToken: string,
	authMode: "bearer" | "api-key",
	override?: { provider?: string; modelId: string },
): ResolvedConfig | null {
	// Runtime override (set via /model) wins over env vars wins over the default.
	const rawProvider = override?.provider ?? env.CODEBASE_PROVIDER;
	const explicitProvider = (rawProvider && rawProvider !== "codebase" ? rawProvider : undefined) as
		| KnownProvider
		| undefined;
	const explicitModel = override?.modelId ?? env.CODEBASE_MODEL;
	const proxyBase = (env.CODEBASE_PROXY_BASE_URL ?? DEFAULT_PROXY_BASE).replace(/\/+$/, "");

	// No provider + no model → "Codebase Auto" (the routed default).
	// No provider + an explicit model id → synthesize an openai-compat model
	// against that id. Pi-ai dispatches by `model.id` in the request body;
	// the proxy's model registry is the gatekeeper for what actually exists.
	if (!explicitProvider) {
		const template = getModel("groq", "llama-3.3-70b-versatile") as Model<string> | undefined;
		if (!template) return null;
		const isDefault = !explicitModel;
		const model = withProxyAuthHeaders(
			{
				...template,
				// "Codebase Auto" — backend renamed this slot from MiniMax-M2.7
				// to d4f (DeepSeek V4 Flash) when the underlying SGLang server
				// was repointed. Keep this in sync with web/backend/providers/
				// registry.js DEFAULT_MODEL.
				id: explicitModel ?? "d4f",
				name: isDefault ? "Codebase Auto" : (explicitModel ?? "Codebase Auto"),
				baseUrl: proxyBase,
				// Override provider so the status bar and /model don't lie about
				// where this is served from. pi-ai uses `provider` mainly for
				// display + a few baseUrl heuristics; the request body sends
				// `model.id` only, so this cast is safe.
				provider: "codebase" as Model<string>["provider"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				// Keep this in sync with web/backend/providers/registry.js. The
				// Groq llama template's context window is only a synthesis
				// scaffold; compaction must budget against the actual d4f backend.
				contextWindow: 131_072,
			},
			accessToken,
			authMode,
		);
		return { model, apiKey: accessToken, source: "proxy", proxyAuth: authMode };
	}

	const modelId = explicitModel ?? DEFAULT_MODELS[explicitProvider];
	if (!modelId) return null;
	const baseModel = getModel(explicitProvider, modelId as never) as Model<string> | undefined;
	if (baseModel) {
		const proxiedModel = withProxyAuthHeaders({ ...baseModel, baseUrl: proxyBase }, accessToken, authMode);
		return { model: proxiedModel, apiKey: accessToken, source: "proxy", proxyAuth: authMode };
	}
	// pi-ai's registry doesn't know this provider+modelId combo (common for
	// backend-only models the proxy exposes, e.g. "codebase:MiniMax-M2.7"
	// or custom in-house ids). The proxy speaks openai-completions for
	// every upstream, so synthesizing an openai-compat model from a known
	// template + the user's chosen id works regardless of whether pi-ai
	// recognizes it locally. Without this fallback, /model would silently
	// revert to Codebase Auto whenever the user picked anything pi-ai
	// didn't have native cost / context-window data for.
	const template = getModel("groq", "llama-3.3-70b-versatile") as Model<string> | undefined;
	if (!template) return null;
	const synthesized = withProxyAuthHeaders(
		{
			...template,
			id: modelId,
			name: modelId,
			baseUrl: proxyBase,
			provider: explicitProvider as Model<string>["provider"],
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
			// Same reasoning as Codebase Auto: synthesized proxy models point
			// at large-context backends. Without overriding contextWindow the
			// Groq llama template's 128k leaks through and the compaction
			// engine triggers ~30% earlier than it should.
			contextWindow: guessContextWindow(modelId, 200_000),
		},
		accessToken,
		authMode,
	);
	return { model: synthesized, apiKey: accessToken, source: "proxy", proxyAuth: authMode };
}

function withProxyAuthHeaders(
	model: Model<string>,
	accessToken: string,
	authMode: "bearer" | "api-key",
): Model<string> {
	if (authMode === "bearer") return model;
	return {
		...model,
		headers: {
			...model.headers,
			"X-API-Key": accessToken,
		},
	};
}

/**
 * Best-effort context-window guess for synthesized proxy models whose
 * IDs pi-ai doesn't know natively. Pattern-matches a few common families
 * so we don't gimp a 1M-context Gemini model to 200k; everything else
 * defaults to the supplied fallback (200k, matching Claude Sonnet 4 /
 * GPT-5 / most open-weight large models the proxy routes to).
 */
function guessContextWindow(modelId: string, fallback: number): number {
	const id = modelId.toLowerCase();
	if (id.startsWith("gemini-")) return 1_000_000;
	if (id.startsWith("gpt-5")) return 400_000;
	if (id.startsWith("claude-")) return 200_000;
	if (id.startsWith("llama-3-3-70b") || id.startsWith("llama-3.3-70b")) return 128_000;
	return fallback;
}

/**
 * OpenAI Chat-Completions compatible custom endpoint. Used by
 * MiniMax in-house, Qwen in-house, Groq custom URLs, Ollama, vLLM,
 * any tinybox running a Chat Completions server. Synthesizes a
 * Model<"openai-completions"> by cloning a known model from the
 * registry and overriding the id + baseUrl. Cost is unknown so we
 * zero it — /cost will report $0 for these runs, which is honest
 * (we don't know the rate) rather than wrong.
 */
function buildOpenAiCompatConfig(opts: {
	baseUrl: string;
	modelId: string;
	apiKey: string;
	contextWindow?: number;
}): ResolvedConfig | null {
	// groq's llama-3.3-70b-versatile uses "openai-completions" — the
	// closest match for what MiniMax / Qwen in-house actually serve.
	const template = getModel("groq", "llama-3.3-70b-versatile") as Model<string> | undefined;
	if (!template) return null;
	const model: Model<string> = {
		...template,
		id: opts.modelId,
		name: opts.modelId,
		baseUrl: opts.baseUrl.replace(/\/+$/, ""),
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		// Detected by the wizard's local-server scan when available — a 32k
		// local model must not inherit the template's 128k or compaction
		// would fire far too late.
		contextWindow: opts.contextWindow ?? template.contextWindow,
	};
	return { model, apiKey: opts.apiKey, source: "explicit" };
}

/**
 * BYOK mode: caller has saved a provider's own API key. Use the
 * provider's normal baseUrl from pi-ai's registry — no proxy. A /model
 * override swaps the id within the same provider; an id pi-ai doesn't
 * know natively is id-cloned from the provider's default so every model
 * the provider's /models endpoint lists is switchable.
 */
function buildByokConfig(
	provider: KnownProvider,
	apiKey: string,
	override?: { provider?: string; modelId: string },
): ResolvedConfig | null {
	const defaultId = DEFAULT_MODELS[provider];
	const wantId = override?.modelId ?? defaultId;
	if (!wantId) return null;
	let model = getModel(provider, wantId as never) as Model<string> | undefined;
	if (!model && defaultId) {
		const template = getModel(provider, defaultId as never) as Model<string> | undefined;
		if (template) {
			model = {
				...template,
				id: wantId,
				name: wantId,
				contextWindow: guessContextWindow(wantId, template.contextWindow),
			};
		}
	}
	if (!model) return null;
	return { model, apiKey, source: "byok" };
}

const OPTION_KEYS = new Set(["env", "credentials", "modelOverride"]);

function isProcessEnv(value: NodeJS.ProcessEnv | ResolveConfigOptions): value is NodeJS.ProcessEnv {
	if (!value) return true;
	// ResolveConfigOptions only ever holds env/credentials/modelOverride; a
	// real ProcessEnv has many other keys. (Missing modelOverride here was a
	// real bug: resolveConfig({modelOverride}) was treated as an env, so the
	// /model override was silently dropped and the model reverted to default.)
	const keys = Object.keys(value);
	if (keys.length === 0) return true;
	return !keys.every((k) => OPTION_KEYS.has(k));
}
