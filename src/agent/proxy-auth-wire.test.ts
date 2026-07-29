import { mkdtempSync, rmSync } from "node:fs";
import { type AddressInfo, createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { completeSimple } from "@earendil-works/pi-ai";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { CredentialsStore } from "../auth/credentials.js";
import { resolveConfig } from "./config.js";

describe("manual proxy authentication on the wire", () => {
	const requests: Array<Record<string, string | string[] | undefined>> = [];
	let server: ReturnType<typeof createServer>;
	let baseUrl: string;

	beforeAll(async () => {
		server = createServer((req, res) => {
			requests.push(req.headers);
			res.writeHead(200, { "Content-Type": "text/event-stream" });
			res.write(
				`data: ${JSON.stringify({
					id: "chatcmpl-auth",
					object: "chat.completion.chunk",
					created: Math.floor(Date.now() / 1000),
					model: "d4f",
					choices: [{ index: 0, delta: { role: "assistant", content: "authenticated" }, finish_reason: null }],
				})}\n\n`,
			);
			res.write(
				`data: ${JSON.stringify({
					id: "chatcmpl-auth",
					object: "chat.completion.chunk",
					created: Math.floor(Date.now() / 1000),
					model: "d4f",
					choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
				})}\n\n`,
			);
			res.end("data: [DONE]\n\n");
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		const address = server.address() as AddressInfo;
		baseUrl = `http://127.0.0.1:${address.port}/inference`;
	});

	afterAll(async () => {
		await new Promise<void>((resolve, reject) => {
			server.close((error) => (error ? reject(error) : resolve()));
		});
	});

	it("sends X-API-Key through the real OpenAI-compatible transport", async () => {
		const dataRoot = mkdtempSync(join(tmpdir(), "codebase-proxy-wire-"));
		try {
			const credentials = new CredentialsStore({ dataRoot });
			credentials.save({
				accessToken: "cb_test_wire",
				scopes: ["inference"],
				source: "manual",
			});
			const config = resolveConfig({
				env: { CODEBASE_PROXY_BASE_URL: baseUrl },
				credentials,
			});

			const response = await completeSimple(
				config.model,
				{ messages: [{ role: "user", content: [{ type: "text", text: "hello" }], timestamp: Date.now() }] },
				{ apiKey: config.apiKey },
			);

			expect(response.content).toContainEqual({ type: "text", text: "authenticated" });
			expect(requests).toHaveLength(1);
			expect(requests[0]["x-api-key"]).toBe("cb_test_wire");
		} finally {
			rmSync(dataRoot, { recursive: true, force: true });
		}
	});
});
