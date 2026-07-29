import type { AgentTool } from "@earendil-works/pi-agent-core";
import type { McpClient } from "./client.js";
import { type LoadMcpConfigOptions, loadMcpServers, type NamedServer } from "./config.js";
import { HttpMcpClient } from "./http-client.js";
import type { AuthorizeDeps } from "./oauth/flow.js";
import { McpOAuthProvider } from "./oauth/provider.js";
import { McpOAuthStore } from "./oauth/store.js";
import type {
	McpGetPromptResult,
	McpPromptDescriptor,
	McpReadResourceResult,
	McpResourceDescriptor,
} from "./protocol.js";
import { StdioMcpClient } from "./stdio-client.js";
import { mcpToAgentTool } from "./to-agent-tool.js";

export interface McpServerStatus {
	name: string;
	connected: boolean;
	toolCount: number;
	error?: string;
}

/** A resource plus the server that exposes it. */
export interface McpResourceRef {
	server: string;
	descriptor: McpResourceDescriptor;
}

/** A prompt plus the server that exposes it. */
export interface McpPromptRef {
	server: string;
	descriptor: McpPromptDescriptor;
}

export interface McpManagerOptions {
	/** Persisted OAuth sessions for remote servers. Defaults to ~/.codebase/mcp-credentials.json. */
	oauthStore?: McpOAuthStore;
	/** How the OAuth flow opens the browser / surfaces the auth URL to the user. */
	authDeps?: AuthorizeDeps;
}

/**
 * Owns the lifecycle of every configured MCP server: spawns each stdio
 * server, runs the handshake, collects its tools, and exposes them as
 * AgentTools the agent can call. A server that fails to connect is
 * recorded (status.error) and skipped — one broken server never blocks
 * the others or the agent's own built-in tools.
 *
 * connectAll() is best-effort and resolves once every server has either
 * connected or failed. dispose() terminates all subprocesses.
 */
export class McpManager {
	private readonly clients: McpClient[] = [];
	private readonly clientsByName = new Map<string, McpClient>();
	private readonly statuses: McpServerStatus[] = [];
	private readonly toolList: AgentTool<any>[] = [];
	private readonly resourceList: McpResourceRef[] = [];
	private readonly promptList: McpPromptRef[] = [];
	private readonly oauthStore: McpOAuthStore;
	private readonly authDeps: AuthorizeDeps;

	constructor(options: McpManagerOptions = {}) {
		this.oauthStore = options.oauthStore ?? new McpOAuthStore();
		this.authDeps = options.authDeps ?? {};
	}

	/**
	 * Load config + connect every configured server (stdio or remote).
	 * Tools are collected as servers come up. Returns once all have
	 * settled. Safe to call when no config exists — yields zero tools.
	 */
	async connectAll(options: LoadMcpConfigOptions = {}): Promise<void> {
		const servers = loadMcpServers(options);
		await this.connectServers(servers);
	}

	/**
	 * Connect an explicit server list supplied by an embedding protocol such
	 * as ACP. Existing configured servers and supplied servers share the same
	 * lifecycle and tool bridge.
	 */
	async connectServers(servers: readonly NamedServer[]): Promise<void> {
		await Promise.all(
			servers.map(async (server) => {
				const { name } = server;
				if (this.clientsByName.has(name)) return;
				const client = this.makeClient(server);
				try {
					await client.connect();
					const descriptors = await client.listTools();
					this.clients.push(client);
					this.clientsByName.set(name, client);
					for (const desc of descriptors) {
						this.toolList.push(mcpToAgentTool(name, client, desc));
					}
					// Resources + prompts are best-effort — a server without the
					// capability returns [], and a failure never blocks its tools.
					const resources = await client.listResources().catch(() => []);
					for (const r of resources) this.resourceList.push({ server: name, descriptor: r });
					const prompts = await client.listPrompts().catch(() => []);
					for (const p of prompts) this.promptList.push({ server: name, descriptor: p });
					this.statuses.push({ name, connected: true, toolCount: descriptors.length });
				} catch (err) {
					client.close();
					this.statuses.push({
						name,
						connected: false,
						toolCount: 0,
						error: err instanceof Error ? err.message : String(err),
					});
				}
			}),
		);
	}

	/** All successfully-bridged MCP tools. */
	tools(): AgentTool<any>[] {
		return this.toolList;
	}

	/** Per-server connection status, for `/mcp` and diagnostics. */
	status(): readonly McpServerStatus[] {
		return this.statuses;
	}

	/** Every resource discovered across connected servers. */
	resources(): readonly McpResourceRef[] {
		return this.resourceList;
	}

	/** Read a resource by server name + URI. Throws if the server isn't connected. */
	async readResource(server: string, uri: string): Promise<McpReadResourceResult> {
		const client = this.clientsByName.get(server);
		if (!client) throw new Error(`MCP server "${server}" is not connected`);
		return client.readResource(uri);
	}

	/** Every prompt discovered across connected servers. */
	prompts(): readonly McpPromptRef[] {
		return this.promptList;
	}

	/** Expand a prompt by server + name with arguments. Throws if the server isn't connected. */
	async getPrompt(server: string, name: string, args: Record<string, string>): Promise<McpGetPromptResult> {
		const client = this.clientsByName.get(server);
		if (!client) throw new Error(`MCP server "${server}" is not connected`);
		return client.getPrompt(name, args);
	}

	/** Terminate every server connection. Idempotent. */
	dispose(): void {
		for (const client of this.clients) client.close();
	}

	/** Build the right transport client for a configured server. */
	private makeClient(server: NamedServer): McpClient {
		if (server.transport === "http") {
			const auth = new McpOAuthProvider(server.name, server.spec.url, this.oauthStore, this.authDeps);
			return new HttpMcpClient(server.name, server.spec, auth);
		}
		return new StdioMcpClient(server.name, server.spec);
	}
}
