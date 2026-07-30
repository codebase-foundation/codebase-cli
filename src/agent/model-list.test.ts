import type { Model } from "@earendil-works/pi-ai";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchAvailableModels } from "./model-list.js";

function model(provider: string, baseUrl?: string): Model<string> {
	return { provider, baseUrl, id: "x", name: "x" } as unknown as Model<string>;
}

function mockFetch(status: number, body: unknown) {
	const fn = vi.fn(async () => ({
		ok: status >= 200 && status < 300,
		status,
		statusText: status === 200 ? "OK" : "ERR",
		json: async () => body,
	}));
	return fn as unknown as typeof fetch;
}

describe("fetchAvailableModels", () => {
	afterEach(() => vi.unstubAllGlobals());

	it("parses an OpenAI-compatible / local server's {data:[{id}]}", async () => {
		vi.stubGlobal("fetch", mockFetch(200, { data: [{ id: "llama3.1" }, { id: "qwen2.5-coder" }] }));
		const out = await fetchAvailableModels(model("openai-compat", "http://localhost:1234/v1"), "key");
		expect(out.map((m) => m.id)).toEqual(["llama3.1", "qwen2.5-coder"]);
		expect(out[0].provider).toBe("openai-compat");
	});

	it("parses the proxy's {models:[{id,name,provider}]} shape", async () => {
		vi.stubGlobal("fetch", mockFetch(200, { models: [{ id: "d4f", name: "Codebase Auto", provider: "codebase" }] }));
		const out = await fetchAvailableModels(model("codebase", "https://codebase.design/api/inference"), "tok");
		expect(out).toEqual([{ id: "d4f", name: "Codebase Auto", provider: "codebase" }]);
	});

	it("preserves model X-API-Key headers for Codebase-issued credentials", async () => {
		const f = mockFetch(200, { models: [] });
		vi.stubGlobal("fetch", f);
		const manualModel = {
			...model("codebase", "https://codebase.design/api/inference"),
			headers: { "X-API-Key": "cb_test_manual" },
		};

		await fetchAvailableModels(manualModel, "cb_test_manual");

		const headers = (f as unknown as ReturnType<typeof vi.fn>).mock.calls[0][1]?.headers;
		expect(headers).toMatchObject({
			"X-API-Key": "cb_test_manual",
		});
		expect(headers.Authorization).toBeUndefined();
	});

	it("hits Anthropic's endpoint and maps display_name", async () => {
		const f = mockFetch(200, { data: [{ id: "claude-opus-4-1", display_name: "Claude Opus 4.1" }] });
		vi.stubGlobal("fetch", f);
		const out = await fetchAvailableModels(model("anthropic"), "sk-ant");
		expect(out).toEqual([{ id: "claude-opus-4-1", name: "Claude Opus 4.1", provider: "anthropic" }]);
		expect(String((f as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0])).toContain("api.anthropic.com");
	});

	it("parses Google models and strips the models/ prefix", async () => {
		vi.stubGlobal(
			"fetch",
			mockFetch(200, {
				models: [
					{
						name: "models/gemini-2.5-pro",
						displayName: "Gemini 2.5 Pro",
						supportedGenerationMethods: ["generateContent"],
					},
					{ name: "models/embedding-001", supportedGenerationMethods: ["embedContent"] },
				],
			}),
		);
		const out = await fetchAvailableModels(model("google"), "key");
		// The embedding model is filtered out (no generateContent support).
		expect(out).toEqual([{ id: "gemini-2.5-pro", name: "Gemini 2.5 Pro", provider: "google" }]);
	});

	it("throws on a non-OK response", async () => {
		vi.stubGlobal("fetch", mockFetch(500, {}));
		await expect(fetchAvailableModels(model("openai-compat", "http://x/v1"), "k")).rejects.toThrow();
	});

	it("returns [] when an openai-compat session has no baseUrl", async () => {
		const out = await fetchAvailableModels(model("openai-compat", undefined), "k");
		expect(out).toEqual([]);
	});
});
