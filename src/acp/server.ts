import { randomUUID } from "node:crypto";
import { isAbsolute, resolve } from "node:path";
import type { Readable, Writable } from "node:stream";
import { Readable as NodeReadable, Writable as NodeWritable } from "node:stream";
import * as acp from "@agentclientprotocol/sdk";
import type { AgentEvent } from "@earendil-works/pi-agent-core";
import type { ImageContent } from "@earendil-works/pi-ai";
import { type AgentBundle, type CreateAgentOptions, createAgent } from "../agent/agent.js";
import type { ModelOption } from "../agent/model-list.js";
import { latestAssistantError, userFacingErrorMessage } from "../errors/user-facing.js";
import type { NamedServer } from "../mcp/config.js";
import type { PermissionRequest, ResponseChoice } from "../permissions/store.js";
import { VERSION } from "../version.js";

interface AcpSession {
	id: string;
	bundle: AgentBundle;
	promptInFlight: boolean;
	cancelled: boolean;
	notificationQueue: Promise<void>;
	notificationError?: unknown;
	assistantText: string;
	cwd: string;
	mcpServers: acp.McpServer[];
	modelOptions: ModelOption[];
}

export interface AcpServerOptions {
	stdin?: Readable;
	stdout?: Writable;
	stderr?: Writable;
	configOverride?: CreateAgentOptions["configOverride"];
}

export async function runAcpServer(options: AcpServerOptions = {}): Promise<number> {
	const stdin = options.stdin ?? process.stdin;
	const stdout = options.stdout ?? process.stdout;
	const sessions = new Map<string, AcpSession>();

	const stream = acp.ndJsonStream(NodeWritable.toWeb(stdout), NodeReadable.toWeb(stdin) as ReadableStream<Uint8Array>);

	const app = acp
		.agent({ name: "codebase-cli" })
		.onRequest("initialize", (ctx) => initialize(ctx.params))
		.onRequest("session/new", (ctx) => newSession(ctx.params))
		.onRequest("session/set_config_option", (ctx) => setSessionConfigOption(ctx.params))
		.onRequest("session/prompt", (ctx) => prompt(ctx.params, ctx.client))
		.onNotification("session/cancel", (ctx) => cancel(ctx.params));

	const connection = app.connect(stream);
	await connection.closed;
	for (const session of sessions.values()) disposeSession(session);
	return 0;

	function initialize(params: acp.InitializeRequest): acp.InitializeResponse {
		return {
			protocolVersion: Math.min(params.protocolVersion, acp.PROTOCOL_VERSION),
			agentInfo: { name: "Codebase", version: VERSION },
			agentCapabilities: {
				loadSession: false,
				promptCapabilities: {
					image: true,
					embeddedContext: true,
				},
				mcpCapabilities: {
					http: true,
				},
			},
		};
	}

	async function newSession(params: acp.NewSessionRequest): Promise<acp.NewSessionResponse> {
		if (!isAbsolute(params.cwd)) throw new Error("ACP session cwd must be an absolute path");

		const bundle = createAgent({
			cwd: params.cwd,
			autoApprove: false,
			persistSession: false,
			configOverride: options.configOverride,
		});

		try {
			await connectMcp(bundle, params.mcpServers, params.cwd);
		} catch (error) {
			disposeBundle(bundle);
			throw error;
		}

		const id = randomUUID();
		const session: AcpSession = {
			id,
			bundle,
			promptInFlight: false,
			cancelled: false,
			notificationQueue: Promise.resolve(),
			assistantText: "",
			cwd: params.cwd,
			mcpServers: [...params.mcpServers],
			modelOptions: await discoverModels(bundle),
		};
		sessions.set(id, session);
		return { sessionId: id, configOptions: [modelConfig(session)] };
	}

	async function setSessionConfigOption(
		params: acp.SetSessionConfigOptionRequest,
	): Promise<acp.SetSessionConfigOptionResponse> {
		const session = sessions.get(params.sessionId);
		if (!session) throw new Error(`ACP session not found: ${params.sessionId}`);
		if (session.promptInFlight) throw new Error("cannot change the model while a prompt is in flight");
		if (params.configId !== "model" || typeof params.value !== "string") {
			throw new Error(`unsupported ACP session config option: ${params.configId}`);
		}
		const selected = session.modelOptions.find((option) => option.id === params.value);
		if (!selected) throw new Error(`unknown model: ${params.value}`);
		if (session.bundle.model.id === selected.id) return { configOptions: [modelConfig(session)] };

		const previous = session.bundle;
		const next = createAgent({
			cwd: session.cwd,
			autoApprove: false,
			persistSession: false,
			initialMessages: [...previous.agent.state.messages],
			taskListId: previous.sessions.id,
			modelOverride: { provider: selected.provider, modelId: selected.id },
			configOverride: options.configOverride,
		});
		try {
			await connectMcp(next, session.mcpServers, session.cwd);
		} catch (error) {
			disposeBundle(next);
			throw error;
		}
		session.bundle = next;
		disposeBundle(previous);
		return { configOptions: [modelConfig(session)] };
	}

	async function prompt(params: acp.PromptRequest, client: acp.AgentContext): Promise<acp.PromptResponse> {
		const session = sessions.get(params.sessionId);
		if (!session) throw new Error(`ACP session not found: ${params.sessionId}`);
		if (session.promptInFlight) throw new Error("a prompt is already in flight for this session");

		session.promptInFlight = true;
		session.cancelled = false;
		session.notificationError = undefined;
		session.assistantText = "";

		const { text, images } = promptContent(params.prompt);
		const permissionTasks = new Set<Promise<void>>();
		const unsubscribeEvents = session.bundle.subscribe((event) => translateEvent(session, event, client));
		const unsubscribePermissions = session.bundle.permissions.subscribe((request) => {
			if (!request) return;
			const task = requestPermission(session, request, client).finally(() => permissionTasks.delete(task));
			permissionTasks.add(task);
		});

		try {
			const result = await session.bundle.submitUserPrompt(text, images);
			await Promise.all(permissionTasks);
			await session.notificationQueue;
			if (session.notificationError) throw session.notificationError;
			if (session.cancelled) return { stopReason: "cancelled" };
			if (!result.submitted) {
				await notifyText(session, client, result.reason ?? "Prompt blocked by Codebase policy.");
				await session.notificationQueue;
				return { stopReason: "refusal" };
			}
			const turnError = result.error ?? latestAssistantError(session.bundle.agent.state.messages);
			if (turnError) {
				const prefix = session.assistantText
					? "\n\nCodebase couldn't complete this turn: "
					: "Codebase couldn't complete this turn: ";
				await notifyText(session, client, `${prefix}${userFacingErrorMessage(turnError)}`);
				await session.notificationQueue;
			}
			return { stopReason: "end_turn" };
		} finally {
			unsubscribeEvents();
			unsubscribePermissions();
			session.promptInFlight = false;
		}
	}

	function cancel(params: acp.CancelNotification): void {
		const session = sessions.get(params.sessionId);
		if (!session) return;
		session.cancelled = true;
		session.bundle.agent.abort();
	}

	function translateEvent(session: AcpSession, event: AgentEvent, client: acp.AgentContext): void {
		switch (event.type) {
			case "message_start":
				if (event.message.role === "assistant") session.assistantText = "";
				break;
			case "message_update":
			case "message_end": {
				if (event.message.role !== "assistant") break;
				const current = assistantMessageText(event.message);
				const delta = current.startsWith(session.assistantText)
					? current.slice(session.assistantText.length)
					: current;
				session.assistantText = current;
				if (delta) void notifyText(session, client, delta);
				break;
			}
			case "tool_execution_start":
				queueNotification(session, client, {
					sessionUpdate: "tool_call",
					toolCallId: event.toolCallId,
					title: toolTitle(event.toolName, event.args),
					name: event.toolName,
					kind: toolKind(event.toolName),
					status: "in_progress",
					locations: toolLocations(event.args, session.bundle.toolContext.cwd),
					rawInput: event.args,
				});
				break;
			case "tool_execution_update":
				queueNotification(session, client, {
					sessionUpdate: "tool_call_update",
					toolCallId: event.toolCallId,
					status: "in_progress",
					rawOutput: boundedValue(event.partialResult),
				});
				break;
			case "tool_execution_end":
				queueNotification(session, client, {
					sessionUpdate: "tool_call_update",
					toolCallId: event.toolCallId,
					status: event.isError ? "failed" : "completed",
					rawOutput: boundedValue(event.result),
				});
				break;
		}
	}

	async function requestPermission(
		session: AcpSession,
		request: PermissionRequest,
		client: acp.AgentContext,
	): Promise<void> {
		try {
			const response = await client.request(acp.methods.client.session.requestPermission, {
				sessionId: session.id,
				toolCall: {
					toolCallId: request.id,
					title: request.summary,
					name: request.tool,
					kind: toolKind(request.tool),
					status: "pending",
					rawInput: {
						reason: request.reason,
						detail: request.detail,
						risk: request.risk,
					},
				},
				options: [
					{ optionId: "allow-once", name: "Allow once", kind: "allow_once" },
					{
						optionId: "trust-tool",
						name: request.trustScope ? `Always allow ${request.trustScope}` : "Always allow this tool",
						kind: "allow_always",
					},
					{ optionId: "deny", name: "Deny", kind: "reject_once" },
				],
			});
			const choice: ResponseChoice =
				response.outcome.outcome === "selected"
					? response.outcome.optionId === "allow-once"
						? "allow-once"
						: response.outcome.optionId === "trust-tool"
							? "trust-tool"
							: "deny"
					: "deny";
			session.bundle.permissions.respond(request.id, choice);
		} catch (error) {
			session.bundle.permissions.respond(request.id, "deny");
			throw error;
		}
	}

	function queueNotification(session: AcpSession, client: acp.AgentContext, update: acp.SessionUpdate): void {
		session.notificationQueue = session.notificationQueue
			.then(() =>
				client.notify(acp.methods.client.session.update, {
					sessionId: session.id,
					update,
				}),
			)
			.catch((error: unknown) => {
				session.notificationError = error;
			});
	}

	async function notifyText(session: AcpSession, client: acp.AgentContext, text: string): Promise<void> {
		queueNotification(session, client, {
			sessionUpdate: "agent_message_chunk",
			messageId: `assistant-${session.id}`,
			content: { type: "text", text },
		});
	}

	function disposeSession(session: AcpSession): void {
		session.bundle.agent.abort();
		disposeBundle(session.bundle);
	}

	function disposeBundle(bundle: AgentBundle): void {
		bundle.mcp.dispose();
		bundle.checkpoints.dispose();
		bundle.toolContext.tasks.dispose();
	}

	async function connectMcp(bundle: AgentBundle, servers: readonly acp.McpServer[], cwd: string): Promise<void> {
		await bundle.connectMcp();
		const supplied = toNamedServers(servers, cwd);
		const existingToolCount = bundle.mcp.tools().length;
		await bundle.mcp.connectServers(supplied);
		const suppliedNames = new Set(supplied.map((server) => server.name));
		const failed = bundle.mcp
			.status()
			.filter((status) => suppliedNames.has(status.name) && !status.connected)
			.map((status) => `${status.name}: ${status.error ?? "connection failed"}`);
		if (failed.length > 0) throw new Error(`ACP-provided MCP server failed: ${failed.join("; ")}`);
		const suppliedTools = bundle.mcp.tools().slice(existingToolCount);
		if (suppliedTools.length > 0) {
			bundle.agent.state.tools = [...bundle.agent.state.tools, ...suppliedTools];
		}
	}

	async function discoverModels(bundle: AgentBundle): Promise<ModelOption[]> {
		const current = {
			id: bundle.model.id,
			name: bundle.model.name,
			provider: String(bundle.model.provider),
		};
		try {
			const discovered = await bundle.availableModels(AbortSignal.timeout(5_000));
			const byId = new Map(discovered.map((option) => [option.id, option]));
			if (!byId.has(current.id)) byId.set(current.id, current);
			return [...byId.values()];
		} catch {
			return [current];
		}
	}

	function modelConfig(session: AcpSession): acp.SessionConfigOption {
		return {
			type: "select",
			id: "model",
			name: "Model",
			category: "model",
			currentValue: session.bundle.model.id,
			options: session.modelOptions.map((option) => ({
				value: option.id,
				name: option.name,
				description: option.provider,
			})),
		};
	}
}

function toNamedServers(servers: readonly acp.McpServer[], cwd: string): NamedServer[] {
	return servers.map((server) => {
		if ("type" in server) {
			if (server.type === "http") {
				return {
					name: server.name,
					transport: "http",
					spec: {
						url: server.url,
						headers: Object.fromEntries(server.headers.map((header) => [header.name, header.value])),
					},
				};
			}
			throw new Error(`unsupported ACP MCP transport "${server.type}" for server "${server.name}"`);
		}
		return {
			name: server.name,
			transport: "stdio",
			spec: {
				command: server.command,
				args: server.args,
				env: Object.fromEntries(server.env.map((entry) => [entry.name, entry.value])),
				cwd,
			},
		};
	});
}

function promptContent(blocks: readonly acp.ContentBlock[]): { text: string; images?: ImageContent[] } {
	const text: string[] = [];
	const images: ImageContent[] = [];
	for (const block of blocks) {
		switch (block.type) {
			case "text":
				text.push(block.text);
				break;
			case "image":
				images.push({ type: "image", data: block.data, mimeType: block.mimeType });
				break;
			case "resource_link":
				text.push(`[Reference: ${block.name}](${block.uri})`);
				break;
			case "resource":
				if ("text" in block.resource) {
					text.push(`Reference ${block.resource.uri}:\n${block.resource.text}`);
				} else {
					text.push(`[Binary reference: ${block.resource.uri}]`);
				}
				break;
			case "audio":
				break;
		}
	}
	return { text: text.join("\n\n").trim(), images: images.length > 0 ? images : undefined };
}

function assistantMessageText(message: unknown): string {
	if (!message || typeof message !== "object") return "";
	const content = (message as { content?: unknown }).content;
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.filter(
			(block): block is { type: "text"; text: string } =>
				typeof block === "object" &&
				block !== null &&
				(block as { type?: unknown }).type === "text" &&
				typeof (block as { text?: unknown }).text === "string",
		)
		.map((block) => block.text)
		.join("");
}

function toolKind(name: string): acp.ToolKind {
	if (/^(read_file|list_files|glob|grep|git_status|git_diff|git_log|code_navigation)$/.test(name)) return "read";
	if (/^(write_file|edit_file|multi_edit|notebook_edit|apply_patch)$/.test(name)) return "edit";
	if (/^(web_fetch|web_search)$/.test(name)) return "fetch";
	if (/^(shell|dispatch_agent)$/.test(name)) return "execute";
	if (/^(save_memory|update_task|create_task)$/.test(name)) return "think";
	return "other";
}

function toolTitle(name: string, args: unknown): string {
	const target = firstString(args, ["path", "file_path", "command", "query", "url"]);
	const label = name.replaceAll("_", " ");
	return target ? `${label}: ${target.slice(0, 100)}` : label;
}

function toolLocations(args: unknown, cwd: string): acp.ToolCallLocation[] | undefined {
	const path = firstString(args, ["path", "file_path"]);
	if (!path) return undefined;
	return [{ path: isAbsolute(path) ? path : resolve(cwd, path) }];
}

function firstString(value: unknown, keys: readonly string[]): string | undefined {
	if (!value || typeof value !== "object") return undefined;
	const record = value as Record<string, unknown>;
	for (const key of keys) {
		if (typeof record[key] === "string" && record[key]) return record[key];
	}
	return undefined;
}

function boundedValue(value: unknown): unknown {
	if (typeof value === "string") return value.length > 100_000 ? `${value.slice(0, 100_000)}\n[truncated]` : value;
	try {
		const json = JSON.stringify(value);
		return json.length > 100_000 ? `${json.slice(0, 100_000)}\n[truncated]` : value;
	} catch {
		return String(value);
	}
}
