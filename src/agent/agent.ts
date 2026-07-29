import { homedir } from "node:os";
import { isAbsolute, join, resolve } from "node:path";
import { Agent, type AgentEvent, type AgentMessage } from "@earendil-works/pi-agent-core";
import type { ImageContent, Model } from "@earendil-works/pi-ai";
import { defaultOAuthConfig } from "../auth/cli.js";
import { CredentialsStore } from "../auth/credentials.js";
import { TokenManager } from "../auth/token-manager.js";
import { CheckpointStore } from "../checkpoint/store.js";
import { CompactionEngine } from "../compaction/engine.js";
import { CompactionMonitor } from "../compaction/monitor.js";
import { getOutputStyle } from "../config/output-styles.js";
import { ConfigStore } from "../config/store.js";
import { DiagnosticsEngine, formatDiagnostics } from "../diagnostics/engine.js";
import { GlueClient, resolveGlueModels } from "../glue/client.js";
import { HookManager } from "../hooks/manager.js";
import { McpManager, type McpServerStatus } from "../mcp/manager.js";
import { MemoryExtractor } from "../memory/extractor.js";
import { buildMemoryAddendum, buildRelevantMemoryReminder } from "../memory/inject.js";
import { MemoryStore } from "../memory/store.js";
import { PermissionStore } from "../permissions/store.js";
import { PlanModeStore } from "../plan/store.js";
import { SessionStore } from "../sessions/store.js";
import type { AssetRegistry } from "../skills/loader.js";
import { buildAssetRegistry } from "../skills/registry-factory.js";
import { loadSubagentDefinitions } from "../subagents/definitions.js";
import { BackgroundShellStore } from "../tools/background-shell-store.js";
import { FileStateCache } from "../tools/file-state-cache.js";
import { MonitorStore } from "../tools/monitor-store.js";
import { buildTools } from "../tools/registry.js";
import { TaskStore } from "../tools/task-store.js";
import type { ToolContext } from "../tools/types.js";
import { UserQueryStore } from "../user-queries/store.js";
import { type ResolvedConfig, resolveConfig } from "./config.js";
import { type Effort, resolveEffort } from "./effort.js";
import { fetchAvailableModels, type ModelOption } from "./model-list.js";
import { buildProjectFilesAddendum } from "./project-files.js";
import { streamProxySafely } from "./safe-stream.js";
import { buildSystemPrompt } from "./system-prompt.js";

const WRITE_TOOL_NAMES: ReadonlySet<string> = new Set(["write_file", "edit_file", "multi_edit", "notebook_edit"]);

/**
 * Tools blocked while plan mode is active. Anything that mutates working-tree
 * state, runs commands, or talks to git's index belongs here. Read tools
 * (read_file, list_files, glob, grep, dispatch_agent, web_*, git read trio)
 * stay available so the agent can investigate and write the plan.
 */
const PLAN_MODE_BLOCKED: ReadonlySet<string> = new Set([
	"write_file",
	"edit_file",
	"multi_edit",
	"notebook_edit",
	"shell",
	"git_commit",
	"git_branch",
	"enter_worktree",
	"exit_worktree",
]);

export interface CreateAgentOptions {
	cwd?: string;
	systemPrompt?: string;
	/** When true, attempt to resume the previous session for this cwd. Default false. */
	resume?: boolean;
	/**
	 * Resume a SPECIFIC session by id (from SessionStore.list()), e.g. via
	 * the /resume picker. Takes precedence over `resume` and skips the
	 * model-match check — the user chose this session deliberately.
	 */
	sessionId?: string;
	/**
	 * When true, every tool call that would prompt the user gets
	 * auto-approved instead. Set this only when there's no human at
	 * the terminal — `codebase run --auto-approve …`, bench harnesses,
	 * CI. The TUI never sets it; auto-approve in interactive mode
	 * defeats the entire permission system.
	 */
	autoApprove?: boolean;
	/**
	 * Test escape hatch. Skip resolveConfig() and inject a pre-built
	 * model + apiKey + source. Used by the E2E harness with pi-ai's
	 * faux provider so tests can run without env vars or credentials.
	 * Production code never sets this.
	 */
	configOverride?: { model: ResolvedConfig["model"]; apiKey: string; source: ResolvedConfig["source"] };
	/**
	 * Runtime model override for proxy/OAuth sessions. Lets the user swap
	 * models via /model without restarting. Format: `{ provider?, modelId }`.
	 * Provider is optional — when omitted, the model id is sent verbatim
	 * through the proxy and the backend's registry resolves it.
	 */
	modelOverride?: { provider?: string; modelId: string };
	/**
	 * Seed the agent's transcript from an in-memory message list rather
	 * than from `~/.codebase/sessions/`. Used by the runtime model switch:
	 * we rebuild the agent with the existing conversation so the user
	 * doesn't lose context, but we don't want to disk-roundtrip.
	 */
	initialMessages?: AgentMessage[];
	/** Extra behavior guidance appended after the default tool-aware prompt. */
	systemPromptAddendum?: string;
	/** Reuse an existing task-list id while rebuilding an in-memory agent. */
	taskListId?: string;
	/** Persist settled turns to the resumable session store. Default true. */
	persistSession?: boolean;
}

export interface AgentBundle {
	agent: Agent;
	model: Model<string>;
	source: ResolvedConfig["source"];
	toolContext: ToolContext;
	permissions: PermissionStore;
	userQueries: UserQueryStore;
	planMode: PlanModeStore;
	memory: MemoryStore;
	/** Background pass that mines settled turns for durable memories. */
	memoryExtractor: MemoryExtractor;
	glue: GlueClient;
	compaction: CompactionEngine;
	compactionMonitor: CompactionMonitor;
	sessions: SessionStore;
	hooks: HookManager;
	diagnostics: DiagnosticsEngine;
	/**
	 * Curated assets — skills, templates, prompts — sourced from
	 * ~/.codebase/{skills,templates,prompts}/ and (when signed in)
	 * codebase.foundation. Consumers read on demand; no auto-merge into
	 * the system prompt today.
	 */
	assets: AssetRegistry;
	subscribe: (listener: (event: AgentEvent) => void) => () => void;
	/**
	 * User-initiated prompt — fires UserPromptSubmit hooks; honors exit-code-2
	 * veto. Callers that originate prompts from real user input should use
	 * this instead of `agent.prompt()` directly.
	 */
	submitUserPrompt: (
		text: string,
		images?: ImageContent[],
	) => Promise<{ submitted: boolean; reason?: string; error?: string }>;
	/**
	 * Set when `--resume` actually loaded a prior session, with its
	 * timestamp + message count so the welcome banner can say
	 * "Resumed from 3h ago · 47 messages". Undefined for fresh sessions.
	 */
	resumedFrom?: { updatedAt: number; messageCount: number };
	/**
	 * The transcript loaded from a resumed session. Pi-agent-core already
	 * has these in its internal state — this exposes them so the UI's
	 * reducer can show the prior conversation on screen, not just in the
	 * model's context. Empty when starting fresh.
	 */
	resumedMessages: AgentMessage[];
	/** Tracks long-running shells the agent spawned via background mode. */
	backgroundShells: BackgroundShellStore;
	/** Push-style line monitors over background shells. App subscribes
	 * here and steers matched lines into the agent as system-reminders. */
	monitors: MonitorStore;
	/** Pre-image snapshots of agent file mutations; backs /rewind. */
	checkpoints: CheckpointStore;
	/** MCP server manager — configured stdio servers + their bridged tools. */
	mcp: McpManager;
	/**
	 * Connect configured MCP servers and splice their tools into the live
	 * agent. Async (spawns subprocesses + handshake), so callers run it
	 * after the bundle is built rather than blocking createAgent. Returns
	 * the per-server connection status for display. Safe no-op when no
	 * mcp.json exists.
	 */
	connectMcp: () => Promise<readonly McpServerStatus[]>;
	/** Discover models available through the resolved provider or Codebase proxy. */
	availableModels: (signal?: AbortSignal) => Promise<ModelOption[]>;
}

export function createAgent(opts: CreateAgentOptions = {}): AgentBundle {
	const cwd = opts.cwd ?? process.cwd();

	// Persisted model preference from `~/.codebase/config.json` (set via
	// `/model`) seeds the override when the caller hasn't passed one
	// explicitly. Explicit runtime overrides still win.
	const persistedConfig = new ConfigStore({ cwd });
	const persistedModel = persistedConfig.preferredModel();
	const effectiveOverride =
		opts.modelOverride ??
		(persistedModel?.modelId ? { provider: persistedModel.provider, modelId: persistedModel.modelId } : undefined);
	const { model, apiKey, source } = opts.configOverride ?? resolveConfig({ modelOverride: effectiveOverride });

	// OAuth-sourced credentials rotate ~hourly; build a refresh-aware getter
	// so the agent never sends a stale access token after the first refresh
	// window. BYOK / explicit / auto sources use the static key passed in.
	const tokenManager =
		source === "proxy"
			? new TokenManager({ store: new CredentialsStore(), oauthConfig: defaultOAuthConfig() })
			: null;
	const getApiKey = tokenManager ? () => tokenManager.getAccessToken() : () => apiKey;

	const config = persistedConfig;
	const permissions = new PermissionStore({
		allowPatterns: config.allowPatterns(),
		denyPatterns: config.denyPatterns(),
		autoApprove: opts.autoApprove,
	});
	const userQueries = new UserQueryStore();
	const planMode = new PlanModeStore();
	const hooks = new HookManager();
	hooks.loadFrom(join(homedir(), ".codebase", "hooks.json"), join(cwd, ".codebase", "hooks.json"));
	const diagnostics = new DiagnosticsEngine({ cwd });
	// PlatformLoader is gated on a real auth session — for now we skip
	// it (LocalLoader still works). A future change wires it once we have
	// a stable endpoint contract and the user is signed in.
	const assets = buildAssetRegistry({ projectRoot: cwd });

	const glueModels = resolveGlueModels({ parentModel: model, parentApiKey: apiKey });
	const glue = new GlueClient({
		fastModel: glueModels.fast,
		smartModel: glueModels.smart,
		getApiKey,
	});
	// Pass model.contextWindow explicitly so proxy-synthesized models
	// (Codebase Auto, custom in-house IDs) get the real window instead of
	// the regex-based fallback in tokens.ts, which would otherwise lock
	// them at 128k and trigger compaction at ~96k on a 200k-context route.
	const compaction = new CompactionEngine({ glue, modelId: model.id, contextWindow: model.contextWindow });
	const compactionMonitor = new CompactionMonitor();
	const sessions = new SessionStore({ cwd });
	const resumed = opts.sessionId ? sessions.loadById(opts.sessionId) : opts.resume ? sessions.load(model.id) : null;
	const memory = new MemoryStore({ cwd, sourceSessionId: sessions.id });

	// Background memory extraction: a cheap-model pass mines settled turns
	// for durable facts. Seeded past any resumed transcript so it only
	// considers new conversation. Uses the fast glue slot to stay cheap.
	const memoryExtractor = new MemoryExtractor({
		store: memory,
		model: glue,
		disabled: process.env.CODEBASE_NO_AUTO_MEMORY === "1",
		startAt: (opts.initialMessages ?? resumed?.messages ?? []).length,
	});

	const backgroundShells = new BackgroundShellStore();
	const monitors = new MonitorStore(backgroundShells);
	const checkpoints = new CheckpointStore({ cwd });

	// Shared tool-call guards. The main agent AND every spawned subagent
	// route through these, so a write-capable subagent's edit_file/shell
	// calls hit the same plan-mode gate, permission prompts, and
	// PreToolUse/PostToolUse/PostEdit hooks as the main loop — subagents
	// are never a permission bypass.
	const guardToolCall = async (
		toolName: string,
		args: unknown,
		signal?: AbortSignal,
	): Promise<{ block: true; reason: string } | undefined> => {
		if (planMode.isActive() && PLAN_MODE_BLOCKED.has(toolName)) {
			return {
				block: true,
				reason:
					`${toolName} is blocked while plan mode is active. ` +
					"Use exit_plan_mode after presenting your plan to regain write access.",
			};
		}
		const preview = permissions.preview(toolName, args);
		if (preview.decision === "block") {
			return { block: true, reason: preview.reason ?? "Blocked by permission policy." };
		}
		const decision = await permissions.evaluate(toolName, args);
		if (decision === "block") {
			return { block: true, reason: "Permission denied by user." };
		}
		const filePath = (args as { path?: string } | undefined)?.path;
		const outcome = await hooks.dispatch(
			"PreToolUse",
			{ event: "PreToolUse", toolName, toolArgs: args, filePath, workingDir: cwd },
			signal,
		);
		if (outcome.blocked) {
			return { block: true, reason: outcome.reason ?? "Blocked by hook." };
		}
		return undefined;
	};

	const dispatchPostToolHooks = async (
		toolName: string,
		args: unknown,
		failure: { isError: boolean; error?: string } = { isError: false },
		signal?: AbortSignal,
	): Promise<void> => {
		const filePath = (args as { path?: string } | undefined)?.path;
		await hooks.dispatch(
			"PostToolUse",
			{ event: "PostToolUse", toolName, toolArgs: args, filePath, workingDir: cwd },
			signal,
		);
		// PostToolUseFailure fires only when the tool errored — lets hooks
		// react to failures (notify, log, roll back) without filtering every
		// PostToolUse for an error flag they can't otherwise see.
		if (failure.isError) {
			await hooks.dispatch(
				"PostToolUseFailure",
				{
					event: "PostToolUseFailure",
					toolName,
					toolArgs: args,
					filePath,
					toolError: failure.error,
					workingDir: cwd,
				},
				signal,
			);
		}
		// PostEdit fires for write-family tools so hooks can run formatters
		// / linters / commit-on-save scripts targeted specifically at file
		// mutations (instead of having to filter inside a generic
		// PostToolUse handler). Skip it on a failed write — there's nothing
		// to format.
		if (!failure.isError && filePath && WRITE_TOOL_NAMES.has(toolName)) {
			await hooks.dispatch(
				"PostEdit",
				{ event: "PostEdit", toolName, toolArgs: args, filePath, workingDir: cwd },
				signal,
			);
		}
	};

	const toolContext: ToolContext = {
		cwd,
		fileStateCache: new FileStateCache(),
		tasks: new TaskStore({ cwd, taskListId: opts.taskListId ?? sessions.id }),
		userQueries,
		planMode,
		memory,
		hooks,
		backgroundShells,
		monitors,
		checkpoints,
		subagentTypes: loadSubagentDefinitions({ cwd }),
		spawnSubagent: ({ systemPrompt: subPrompt, tools: subTools, model: modelId, thinkingLevel }) =>
			new Agent({
				initialState: {
					// A per-agent model id reuses the parent's routing (provider,
					// baseUrl, key) and only swaps the id — works for proxy and
					// same-provider BYOK. Cross-provider would need its own key, so
					// it's deliberately out of scope.
					model: modelId ? { ...model, id: modelId } : model,
					systemPrompt: subPrompt,
					tools: subTools,
					...(thinkingLevel && { thinkingLevel: thinkingLevel as Effort }),
				},
				getApiKey,
				...(source === "proxy" && { streamFn: streamProxySafely }),
				beforeToolCall: (ctx, signal) => guardToolCall(ctx.toolCall.name, ctx.args, signal),
				afterToolCall: async (ctx, signal) => {
					await dispatchPostToolHooks(
						ctx.toolCall.name,
						ctx.args,
						{ isError: ctx.isError, error: ctx.isError ? toolResultText(ctx.result) : undefined },
						signal,
					);
					return undefined;
				},
			}),
	};

	// Build tools once so we can both register them with the Agent AND
	// inject a one-liner per tool into the system prompt — saves the model
	// from discovering tool surface area through trial and error.
	const tools = buildTools(toolContext);

	const systemPrompt =
		opts.systemPrompt ??
		buildSystemPrompt({
			tools: tools.map((t) => ({ name: t.name, description: t.description })),
		});
	const systemPromptAddendum = opts.systemPromptAddendum?.trim() ? `\n\n${opts.systemPromptAddendum.trim()}` : "";

	// MEMORY.md gets concatenated onto the system prompt at agent creation;
	// edits during a session don't take effect until next launch.
	// Project-instruction file (first of AGENTS.md / CLAUDE.md / CODEX.md /
	// .cursorrules) gets pinned to the prompt so the agent sees the
	// project's conventions on every turn. Memory addendum is appended
	// after — it's the user's accumulated long-term notes.
	const fullSystemPrompt =
		systemPrompt +
		systemPromptAddendum +
		buildProjectFilesAddendum(cwd) +
		buildMemoryAddendum(memory) +
		buildOutputStyleAddendum(persistedConfig, cwd);

	const effort = resolveEffort(persistedConfig.effort());
	const agent = new Agent({
		initialState: {
			model,
			systemPrompt: fullSystemPrompt,
			tools,
			messages: opts.initialMessages ?? resumed?.messages ?? [],
			...(effort && { thinkingLevel: effort }),
		},
		getApiKey,
		...(source === "proxy" && { streamFn: streamProxySafely }),
		transformContext: async (messages, signal) => {
			if (!compaction.needsCompaction(messages)) return withRelevantMemoryReminder(memory, messages);

			// Stage 1 — microcompaction: clear stale tool-result content
			// (old reads, grep dumps, command output) without a summary
			// round-trip. Cheap. If that alone drops us back under the
			// threshold, we're done and skip the expensive summarize path.
			const micro = compaction.microcompact(messages);
			if (micro.clearedCount > 0 && !compaction.needsCompaction(micro.messages)) {
				return withRelevantMemoryReminder(memory, micro.messages);
			}
			// Microcompaction wasn't enough (or freed nothing) — fall through
			// to the full summarize-everything compaction, operating on the
			// already-cleared messages so the summary input is smaller too.
			const working = micro.messages;

			compactionMonitor.start(working.length);
			try {
				await hooks.dispatch(
					"PreCompact",
					{ event: "PreCompact", workingDir: cwd, messageCount: working.length },
					signal,
				);
				const result = await compaction.compact(working, signal);
				await hooks.dispatch(
					"PostCompact",
					{
						event: "PostCompact",
						workingDir: cwd,
						messageCount: result.messages.length,
						collapsedMessageCount: result.details.collapsedMessageCount,
						truncatedTokens: result.details.truncatedTokens,
					},
					signal,
				);
				return withRelevantMemoryReminder(memory, result.messages);
			} finally {
				// Always clear — even if compact() threw, the user shouldn't
				// see a stuck "Compacting…" banner forever.
				compactionMonitor.end();
			}
		},
		beforeToolCall: (ctx, signal) => guardToolCall(ctx.toolCall.name, ctx.args, signal),
		afterToolCall: async (ctx, signal) => {
			await dispatchPostToolHooks(
				ctx.toolCall.name,
				ctx.args,
				{ isError: ctx.isError, error: ctx.isError ? toolResultText(ctx.result) : undefined },
				signal,
			);
			const filePath = (ctx.args as { path?: string } | undefined)?.path;

			// After a write/edit tool, run language checkers on the affected file
			// and steer the result into the next turn. Fire-and-forget so the
			// tool result return isn't blocked by a 15s checker run.
			if (filePath && WRITE_TOOL_NAMES.has(ctx.toolCall.name)) {
				const absPath = isAbsolute(filePath) ? filePath : resolve(cwd, filePath);
				diagnostics
					.forFiles([absPath], signal)
					.then((diags) => {
						if (diags.length === 0) return;
						const body = formatDiagnostics(diags);
						agentRef?.steer({
							role: "user",
							content: `<system-reminder>\n${body}\n</system-reminder>`,
							timestamp: Date.now(),
						});
					})
					.catch((err) => {
						// Diagnostics failures are non-fatal — but always-stderr
						// instead of debug-only, because the previous behavior
						// (silent under default settings) hid a real production
						// bug where a checker hung and the user thought the tool
						// itself was slow. Visible to anyone watching the terminal;
						// the agent keeps running.
						process.stderr.write(
							`[diagnostics] ${absPath}: ${err instanceof Error ? err.message : String(err)}\n`,
						);
					});
			}
			return undefined;
		},
	});

	// agentRef lets the afterToolCall closure call agent.steer() once the
	// Agent is constructed. JS hoisting makes the assignment safe because
	// afterToolCall fires inside the event loop, well after this assignment.
	const agentRef: Agent = agent;

	const subscribe = (listener: (event: AgentEvent) => void): (() => void) =>
		agent.subscribe((event) => {
			listener(event);
		});

	// Persist after every agent_end so a crash mid-session doesn't lose work.
	agent.subscribe((event) => {
		if (event.type !== "agent_end") return;
		if (opts.persistSession === false) return;
		try {
			const messages = event.messages.length > 0 ? event.messages : (resumed?.messages ?? []);
			if (messages.length === 0) return;
			sessions.save({
				modelId: model.id,
				title: resumed?.title ?? null,
				messages,
				usage: resumed?.usage ?? {
					input: 0,
					output: 0,
					cacheRead: 0,
					cacheWrite: 0,
					totalTokens: 0,
					cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
				},
			});
		} catch (err) {
			// Persistence is best-effort — never crash the agent over a write
			// failure. But silent failure used to mean the user lost work to
			// a full disk with no warning, so surface to stderr so support is
			// possible. Visible to anyone watching the terminal; the agent
			// keeps running.
			const msg = err instanceof Error ? err.message : String(err);
			process.stderr.write(`[session] save failed (${sessions.filePath}): ${msg}\n`);
		}

		// Stop fires once the agent settles after a turn — useful for "ping
		// my phone when the long task finishes" style hooks. Fire-and-forget
		// so a misconfigured hook can't gate the agent_end notification.
		const finalMessage = lastAssistantText(event.messages);
		void hooks.dispatch("Stop", { event: "Stop", workingDir: cwd, finalMessage }).catch(() => undefined);
	});

	// SessionStart fires once per createAgent. Lets hooks pre-seed context
	// (e.g. "add project status to memory") before any user prompt lands.
	// Fire-and-forget because nothing else is waiting on it.
	void hooks.dispatch("SessionStart", { event: "SessionStart", workingDir: cwd }).catch(() => undefined);

	/**
	 * Submit a user-initiated prompt — fires UserPromptSubmit through the
	 * hook chain first so audit / lint hooks can veto with exit code 2.
	 * Subagent prompts (dispatch-agent) skip this and call agent.prompt
	 * directly because they aren't user-initiated.
	 *
	 * Returns `{ submitted: false, reason }` when a sync hook blocked the
	 * submit, otherwise resolves to `{ submitted: true }` after the agent
	 * accepted the prompt. The agent's own turn lifecycle still emits
	 * events on bundle.subscribe.
	 */
	const submitUserPrompt = async (
		text: string,
		images?: ImageContent[],
	): Promise<{ submitted: boolean; reason?: string; error?: string }> => {
		const outcome = await hooks.dispatch("UserPromptSubmit", {
			event: "UserPromptSubmit",
			workingDir: cwd,
			userPrompt: text,
		});
		if (outcome.blocked) {
			return { submitted: false, reason: outcome.reason };
		}
		// Await the agent's turn so headless callers can chain on a real
		// promise that reflects when the conversation has settled. Interactive
		// callers don't await us — they subscribe to bundle.subscribe for the
		// streaming events independent of this resolution.
		try {
			await agent.prompt(text, images && images.length > 0 ? images : undefined);
		} catch (e) {
			// Throws that fire BEFORE agent_start (auth misconfigured, model
			// rejected, network refused at the SDK boundary) never produce an
			// agent_end event, so the bundle.subscribe stream sees nothing and
			// the UI hangs silently. Surface them on the result so the caller
			// can render an error card / status note.
			return { submitted: true, error: e instanceof Error ? e.message : String(e) };
		}
		return { submitted: true };
	};

	// MCP: configured servers (stdio + remote) are connected lazily after
	// the bundle is built (connect is async; createAgent is sync). On
	// connect we splice the bridged tools into the live agent's tool set so
	// the model can call them from the next turn. A remote server that
	// requires OAuth opens the browser on first connect; we also print the
	// URL so headless sessions can complete the flow by hand.
	const mcp = new McpManager({
		authDeps: {
			onAuthUrl: (url, server) => {
				process.stderr.write(
					`[mcp] "${server}" requires sign-in. Opening your browser…\n` +
						`      If it doesn't open, visit this URL to authorize:\n      ${url}\n`,
				);
			},
		},
	});
	// Back the list_mcp_resources / read_mcp_resource tools. Assigned now
	// (after the manager exists); resources populate once connectMcp runs.
	toolContext.mcp = mcp;
	const connectMcp = async (): Promise<readonly McpServerStatus[]> => {
		await mcp.connectAll({ cwd });
		const mcpTools = mcp.tools();
		if (mcpTools.length > 0) {
			agent.state.tools = [...agent.state.tools, ...mcpTools];
		}
		return mcp.status();
	};
	const availableModels = async (signal?: AbortSignal): Promise<ModelOption[]> =>
		fetchAvailableModels(model, await getApiKey(), signal);

	void agentRef;
	return {
		agent,
		model,
		source,
		toolContext,
		permissions,
		userQueries,
		planMode,
		memory,
		memoryExtractor,
		glue,
		compaction,
		compactionMonitor,
		sessions,
		hooks,
		diagnostics,
		assets,
		subscribe,
		submitUserPrompt,
		resumedFrom: resumed ? { updatedAt: resumed.updatedAt, messageCount: resumed.messages.length } : undefined,
		resumedMessages: opts.initialMessages ?? resumed?.messages ?? [],
		backgroundShells: toolContext.backgroundShells,
		monitors: toolContext.monitors,
		checkpoints,
		mcp,
		connectMcp,
		availableModels,
	};
}

/**
 * Build the output-style addendum appended to the system prompt. When a
 * style is selected in config and resolvable from
 * ~/.codebase/output-styles or <cwd>/.codebase/output-styles, its body
 * is wrapped in a labeled section so the model treats it as formatting
 * guidance. Returns "" when no style is active or the named style is
 * missing (e.g. config points at a deleted file).
 */
function buildOutputStyleAddendum(config: ConfigStore, cwd: string): string {
	const id = config.outputStyle();
	if (!id) return "";
	const style = getOutputStyle(id, { cwd });
	if (!style) return "";
	return `\n\n# Response style: ${style.name}\n${style.body}`;
}

function withRelevantMemoryReminder(memory: MemoryStore, messages: AgentMessage[]): AgentMessage[] {
	const target = latestRealUserMessage(messages);
	if (!target) return messages;
	const reminder = buildRelevantMemoryReminder(memory, target.text, { recordUsage: true });
	if (!reminder) return messages;
	const reminderMessage: AgentMessage = {
		role: "user",
		content: reminder,
		timestamp: Date.now(),
	};
	return [...messages.slice(0, target.index), reminderMessage, ...messages.slice(target.index)];
}

function latestRealUserMessage(messages: readonly AgentMessage[]): { index: number; text: string } | null {
	for (let i = messages.length - 1; i >= 0; i--) {
		const message = messages[i];
		if (!message || !("role" in message) || message.role !== "user") continue;
		const text = userMessageText(message.content).trim();
		if (!text || text.startsWith("<system-reminder>")) continue;
		return { index: i, text };
	}
	return null;
}

function userMessageText(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.flatMap((block) => {
			if (!block || typeof block !== "object") return [];
			const candidate = block as { type?: unknown; text?: unknown };
			return candidate.type === "text" && typeof candidate.text === "string" ? [candidate.text] : [];
		})
		.join("\n");
}

/** Extract the trailing assistant text content from an array of messages. */
/** Flatten a tool result's text blocks into a single string for hook payloads. */
function toolResultText(result: { content: readonly { type: string }[] }): string | undefined {
	const text = result.content
		.filter((b): b is { type: "text"; text: string } => b.type === "text")
		.map((b) => b.text)
		.join("\n")
		.trim();
	return text || undefined;
}

function lastAssistantText(messages: AgentMessage[]): string | undefined {
	for (let i = messages.length - 1; i >= 0; i--) {
		const m = messages[i];
		if (m.role !== "assistant") continue;
		if (typeof m.content === "string") return m.content || undefined;
		if (Array.isArray(m.content)) {
			const text = m.content
				.filter((b): b is { type: "text"; text: string } => (b as { type: string }).type === "text")
				.map((b) => b.text)
				.join("");
			return text || undefined;
		}
		return undefined;
	}
	return undefined;
}
