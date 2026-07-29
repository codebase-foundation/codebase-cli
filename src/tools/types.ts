import type { Agent, AgentTool } from "@earendil-works/pi-agent-core";
import type { TSchema } from "typebox";
import type { CheckpointStore } from "../checkpoint/store.js";
import type { HookManager } from "../hooks/manager.js";
import type { McpManager } from "../mcp/manager.js";
import type { MemoryStore } from "../memory/store.js";
import type { PlanModeStore } from "../plan/store.js";
import type { SubagentDefinition } from "../subagents/definitions.js";
import type { UserQueryStore } from "../user-queries/store.js";
import type { BackgroundShellStore } from "./background-shell-store.js";
import type { FileStateCache } from "./file-state-cache.js";
import type { MonitorStore } from "./monitor-store.js";
import type { TaskStore } from "./task-store.js";

/**
 * Per-agent-instance services available to every tool. Threaded through
 * tool factories so individual tools stay testable in isolation.
 */
export interface ToolContext {
	cwd: string;
	/**
	 * Authenticated Codebase platform access. Present for OAuth/manual proxy
	 * sessions so tools can use hosted services without exposing service keys.
	 */
	platform?: {
		baseUrl: string;
		getAuthHeaders: () => Promise<Record<string, string>>;
	};
	fileStateCache: FileStateCache;
	tasks: TaskStore;
	userQueries: UserQueryStore;
	planMode: PlanModeStore;
	memory: MemoryStore;
	/** Long-running shells the agent spawned with `shell({ background: true })`. */
	backgroundShells: BackgroundShellStore;
	/** Registered line-monitors over background shells. Drives push-style
	 * notifications instead of polling shell_output. */
	monitors: MonitorStore;
	/**
	 * Pre-image snapshots of files the agent mutates, for /rewind.
	 * Optional so the test harness and read-only subagents can omit it.
	 */
	checkpoints?: CheckpointStore;
	/**
	 * Available subagent types (built-in + user/project-defined), consumed
	 * by dispatch_agent. Optional so the test harness can omit it.
	 */
	subagentTypes?: readonly SubagentDefinition[];
	/**
	 * Connected MCP manager — backs the list_mcp_resources /
	 * read_mcp_resource tools. Assigned after the manager is built (it
	 * connects async); undefined in the test harness.
	 */
	mcp?: McpManager;
	/**
	 * User-defined hooks. Tools that care about lifecycle events (e.g.
	 * dispatch_agent → SubagentStart/Stop) reach into this. Optional so
	 * the test harness can leave it undefined.
	 */
	hooks?: HookManager;
	/**
	 * Spawn a fresh Agent for sub-tasks (used by dispatch_agent). The
	 * factory inherits the parent's model and apiKey but takes its own
	 * system prompt and tool set — that's how the read-only subagent
	 * isolation is enforced.
	 */
	spawnSubagent: (config: SpawnSubagentConfig) => Agent;
}

export interface SpawnSubagentConfig {
	systemPrompt: string;
	tools: AgentTool<TSchema>[];
	/** Route this subagent through a different model id (same provider/proxy). */
	model?: string;
	/** Reasoning level for the subagent's turns (pi-agent-core ThinkingLevel). */
	thinkingLevel?: string;
}

/** A tool definition + its dependency context. Builders return one of these. */
export type ToolFactory = (ctx: ToolContext) => AgentTool<TSchema>;
