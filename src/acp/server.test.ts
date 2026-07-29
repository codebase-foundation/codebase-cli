import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough, Readable, Writable } from "node:stream";
import { fileURLToPath } from "node:url";
import * as acp from "@agentclientprotocol/sdk";
import type { Model } from "@earendil-works/pi-ai";
import { fauxAssistantMessage, fauxToolCall, registerFauxProvider } from "@earendil-works/pi-ai";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CredentialsStore } from "../auth/credentials.js";
import { runAcpServer } from "./server.js";

const MOCK_MCP = fileURLToPath(new URL("../mcp/__test__/mock-server.mjs", import.meta.url));

describe("runAcpServer", () => {
	let faux: ReturnType<typeof registerFauxProvider>;
	let model: Model<string>;
	let home: string;
	let cwd: string;
	let previousHome: string | undefined;

	beforeEach(() => {
		previousHome = process.env.HOME;
		home = mkdtempSync(join(tmpdir(), "acp-home-"));
		cwd = mkdtempSync(join(tmpdir(), "acp-cwd-"));
		process.env.HOME = home;
		process.env.CODEBASE_NO_AUTO_MEMORY = "1";
		faux = registerFauxProvider({
			models: [
				{
					id: "test-model",
					name: "Test Model",
					reasoning: false,
					input: ["text"],
					cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
					contextWindow: 100_000,
					maxTokens: 4096,
				},
			],
			tokenSize: { min: 1, max: 2 },
		});
		model = faux.models[0] as Model<string>;
	});

	afterEach(() => {
		faux.unregister();
		rmSync(home, { recursive: true, force: true });
		rmSync(cwd, { recursive: true, force: true });
		if (previousHome === undefined) delete process.env.HOME;
		else process.env.HOME = previousHome;
		delete process.env.CODEBASE_NO_AUTO_MEMORY;
	});

	it("runs a Buzz-compatible ACP turn with injected MCP tools and streamed updates", async () => {
		faux.setResponses([
			fauxAssistantMessage([fauxToolCall("mcp__buzz__echo", { text: "from Buzz" })], {
				stopReason: "toolUse",
			}),
			fauxAssistantMessage("posted through Buzz"),
		]);

		const toAgent = new PassThrough();
		const fromAgent = new PassThrough();
		const serverDone = runAcpServer({
			stdin: toAgent,
			stdout: fromAgent,
			configOverride: { model, apiKey: "faux-key", source: "byok" },
		});
		const updates: acp.SessionNotification[] = [];
		const permissionRequests: acp.RequestPermissionRequest[] = [];
		const stream = acp.ndJsonStream(Writable.toWeb(toAgent), Readable.toWeb(fromAgent) as ReadableStream<Uint8Array>);

		const result = await acp
			.client({ name: "buzz-test" })
			.onNotification(acp.methods.client.session.update, (ctx) => {
				updates.push(ctx.params);
			})
			.onRequest(acp.methods.client.session.requestPermission, (ctx) => {
				permissionRequests.push(ctx.params);
				const allow = ctx.params.options.find((option) => option.kind === "allow_once");
				if (!allow) return { outcome: { outcome: "cancelled" as const } };
				return { outcome: { outcome: "selected" as const, optionId: allow.optionId } };
			})
			.connectWith(stream, async (client) => {
				const initialized = await client.request(acp.methods.agent.initialize, {
					protocolVersion: acp.PROTOCOL_VERSION,
					clientInfo: { name: "buzz-acp", version: "test" },
				});
				expect(initialized.protocolVersion).toBe(acp.PROTOCOL_VERSION);
				expect(initialized.agentInfo?.name).toBe("Codebase");

				const session = await client.request(acp.methods.agent.session.new, {
					cwd,
					mcpServers: [
						{
							name: "buzz",
							command: process.execPath,
							args: [MOCK_MCP],
							env: [],
						},
					],
				});
				expect(session.configOptions?.[0]).toMatchObject({
					id: "model",
					category: "model",
					currentValue: "test-model",
				});
				await client.request(acp.methods.agent.session.setConfigOption, {
					sessionId: session.sessionId,
					configId: "model",
					value: "test-model",
				});
				return client.request(acp.methods.agent.session.prompt, {
					sessionId: session.sessionId,
					prompt: [{ type: "text", text: "Reply through the Buzz tool." }],
				});
			});

		expect(result.stopReason).toBe("end_turn");
		expect(permissionRequests).toHaveLength(1);
		expect(permissionRequests[0]?.toolCall.name).toBe("mcp__buzz__echo");
		expect(updates.some((event) => event.update.sessionUpdate === "tool_call")).toBe(true);
		expect(updates.some((event) => event.update.sessionUpdate === "tool_call_update")).toBe(true);
		const text = updates
			.filter(
				(
					event,
				): event is acp.SessionNotification & {
					update: { sessionUpdate: "agent_message_chunk"; content: { type: "text"; text: string } };
				} => event.update.sessionUpdate === "agent_message_chunk" && event.update.content.type === "text",
			)
			.map((event) => event.update.content.text)
			.join("");
		expect(text).toContain("posted through Buzz");

		toAgent.end();
		await serverDone;
	});

	it("refreshes OAuth credentials before a long-lived server creates a new session", async () => {
		const store = new CredentialsStore();
		store.save({
			accessToken: "access-current",
			refreshToken: "refresh-current",
			expiresAt: Date.now() + 4 * 60_000,
			scopes: ["inference"],
			source: "codebase",
		});
		const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
			new Response(JSON.stringify({ access_token: "access-new", expires_in: 3600 }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);

		const toAgent = new PassThrough();
		const fromAgent = new PassThrough();
		const serverDone = runAcpServer({
			stdin: toAgent,
			stdout: fromAgent,
			configOverride: { model, apiKey: "faux-key", source: "byok" },
		});
		const stream = acp.ndJsonStream(Writable.toWeb(toAgent), Readable.toWeb(fromAgent) as ReadableStream<Uint8Array>);

		await acp.client({ name: "buzz-refresh-test" }).connectWith(stream, async (client) => {
			await client.request(acp.methods.agent.initialize, {
				protocolVersion: acp.PROTOCOL_VERSION,
				clientInfo: { name: "buzz-acp", version: "test" },
			});
			await client.request(acp.methods.agent.session.new, {
				cwd,
				mcpServers: [],
			});
		});

		const refreshCalls = fetchSpy.mock.calls.filter(([input]) => String(input).endsWith("/api/oauth/token"));
		expect(refreshCalls).toHaveLength(1);
		expect(store.load()?.accessToken).toBe("access-new");

		toAgent.end();
		await serverDone;
	});

	it("streams provider failures as visible ACP messages", async () => {
		faux.setResponses([
			fauxAssistantMessage([], {
				stopReason: "error",
				errorMessage: '402 "insufficient_credits"',
			}),
		]);

		const toAgent = new PassThrough();
		const fromAgent = new PassThrough();
		const serverDone = runAcpServer({
			stdin: toAgent,
			stdout: fromAgent,
			configOverride: { model, apiKey: "faux-key", source: "byok" },
		});
		const updates: acp.SessionNotification[] = [];
		const stream = acp.ndJsonStream(Writable.toWeb(toAgent), Readable.toWeb(fromAgent) as ReadableStream<Uint8Array>);

		const result = await acp
			.client({ name: "buzz-error-test" })
			.onNotification(acp.methods.client.session.update, (ctx) => {
				updates.push(ctx.params);
			})
			.connectWith(stream, async (client) => {
				await client.request(acp.methods.agent.initialize, {
					protocolVersion: acp.PROTOCOL_VERSION,
					clientInfo: { name: "buzz-acp", version: "test" },
				});
				const session = await client.request(acp.methods.agent.session.new, {
					cwd,
					mcpServers: [],
				});
				return client.request(acp.methods.agent.session.prompt, {
					sessionId: session.sessionId,
					prompt: [{ type: "text", text: "Reply even if the provider fails." }],
				});
			});

		expect(result.stopReason).toBe("end_turn");
		const text = updates
			.filter(
				(
					event,
				): event is acp.SessionNotification & {
					update: { sessionUpdate: "agent_message_chunk"; content: { type: "text"; text: string } };
				} => event.update.sessionUpdate === "agent_message_chunk" && event.update.content.type === "text",
			)
			.map((event) => event.update.content.text)
			.join("");
		expect(text).toContain("Codebase couldn't complete this turn");
		expect(text).toContain("Codebase credits are exhausted");

		toAgent.end();
		await serverDone;
	});
});
