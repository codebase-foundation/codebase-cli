import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CredentialsStore } from "../auth/credentials.js";
import { resolveConfig } from "./config.js";

describe("resolveConfig", () => {
	let dataRoot: string;
	let credentials: CredentialsStore;

	beforeEach(() => {
		dataRoot = mkdtempSync(join(tmpdir(), "config-"));
		credentials = new CredentialsStore({ dataRoot });
	});

	afterEach(() => {
		rmSync(dataRoot, { recursive: true, force: true });
	});

	it("uses byok credentials directly without the proxy", () => {
		credentials.save({
			accessToken: "sk-ant-test",
			scopes: [],
			source: "byok",
			provider: "anthropic",
		});

		const config = resolveConfig({ env: {}, credentials });

		expect(config.source).toBe("byok");
		expect(config.apiKey).toBe("sk-ant-test");
		expect(config.model.provider).toBe("anthropic");
		// The proxy baseUrl is set explicitly only for source=proxy. byok
		// must keep the provider's default baseUrl from pi-ai's registry.
		expect(config.model.baseUrl).not.toContain("codebase.foundation");
		expect(config.model.baseUrl).not.toContain("codebase.design");
	});

	it("resolves a stored openai-compat endpoint to a synthesized model", () => {
		credentials.save({
			accessToken: "none",
			scopes: [],
			source: "byok",
			provider: "openai-compat",
			baseUrl: "http://localhost:11434/v1",
			model: "llama3.3:70b",
		});

		const config = resolveConfig({ env: {}, credentials });

		expect(config.source).toBe("byok");
		expect(config.apiKey).toBe("none");
		expect(config.model.id).toBe("llama3.3:70b");
		expect(config.model.baseUrl).toBe("http://localhost:11434/v1");
	});

	it("defaults OAuth credentials to Codebase Auto", () => {
		credentials.save({
			accessToken: "oauth-token",
			scopes: ["inference"],
			source: "codebase",
		});

		const config = resolveConfig({ env: {}, credentials });

		expect(config.source).toBe("proxy");
		expect(config.apiKey).toBe("oauth-token");
		expect(config.model.provider).toBe("codebase");
		expect(config.model.id).toBe("d4f");
		expect(config.model.name).toBe("Codebase Auto");
		expect(config.model.baseUrl).toBe("https://codebase.design/api/inference");
		expect(config.model.contextWindow).toBe(131_072);
	});

	it("constructs a proxy agent from expired OAuth credentials that can refresh", () => {
		credentials.save({
			accessToken: "expired-oauth-token",
			refreshToken: "refresh-token",
			expiresAt: Date.now() - 60_000,
			scopes: ["inference"],
			source: "codebase",
		});

		const config = resolveConfig({ env: {}, credentials });

		expect(config.source).toBe("proxy");
		expect(config.apiKey).toBe("expired-oauth-token");
		expect(config.model.provider).toBe("codebase");
	});

	it("uses X-API-Key transport for a manually entered Codebase API key", () => {
		credentials.save({
			accessToken: "cb_test_manual",
			scopes: ["inference"],
			source: "manual",
		});

		const config = resolveConfig({ env: {}, credentials });

		expect(config.source).toBe("proxy");
		expect(config.proxyAuth).toBe("api-key");
		expect(config.model.headers).toMatchObject({ "X-API-Key": "cb_test_manual" });
	});

	it("does not use an expired manual token that cannot refresh", () => {
		credentials.save({
			accessToken: "expired-manual-token",
			expiresAt: Date.now() - 60_000,
			scopes: ["inference"],
			source: "manual",
		});

		expect(() => resolveConfig({ env: {}, credentials })).toThrow();
	});

	it("treats provider=codebase model overrides as proxy-routed model ids", () => {
		credentials.save({
			accessToken: "oauth-token",
			scopes: ["inference"],
			source: "codebase",
		});

		const config = resolveConfig({
			env: {},
			credentials,
			modelOverride: { provider: "codebase", modelId: "deepseek-r1:14b" },
		});

		expect(config.source).toBe("proxy");
		expect(config.model.provider).toBe("codebase");
		expect(config.model.id).toBe("deepseek-r1:14b");
		expect(config.model.baseUrl).toBe("https://codebase.design/api/inference");
	});

	it("a scan-detected context window overrides the template default", () => {
		credentials.save({
			accessToken: "none",
			scopes: [],
			source: "byok",
			provider: "openai-compat",
			baseUrl: "http://localhost:1234/v1",
			model: "qwen2.5-coder-32b",
			contextWindow: 32768,
		});

		const config = resolveConfig({ env: {}, credentials });

		expect(config.model.contextWindow).toBe(32768);
	});

	it("honors a /model override on an openai-compat (local) session", () => {
		credentials.save({
			accessToken: "none",
			scopes: [],
			source: "byok",
			provider: "openai-compat",
			baseUrl: "http://localhost:1234/v1",
			model: "qwen2.5-coder-32b",
		});

		const config = resolveConfig({ env: {}, credentials, modelOverride: { modelId: "deepseek-r1:14b" } });

		expect(config.source).toBe("byok");
		expect(config.model.id).toBe("deepseek-r1:14b"); // swapped via /model
		expect(config.model.baseUrl).toBe("http://localhost:1234/v1"); // same local endpoint
	});

	it("honors a /model override on a keyed BYOK session, id-cloning unknown ids", () => {
		credentials.save({ accessToken: "sk-ant-test", scopes: [], source: "byok", provider: "anthropic" });

		const config = resolveConfig({ env: {}, credentials, modelOverride: { modelId: "claude-future-9000" } });

		expect(config.source).toBe("byok");
		expect(config.model.provider).toBe("anthropic");
		expect(config.model.id).toBe("claude-future-9000"); // not in pi-ai's registry, id-cloned
		expect(config.apiKey).toBe("sk-ant-test");
	});

	it("byok credentials win over env-var auto-detect", () => {
		credentials.save({
			accessToken: "sk-or-byok",
			scopes: [],
			source: "byok",
			provider: "openrouter",
		});
		// Anthropic key in env would normally be auto-detected first, but
		// saved credentials take priority.
		const config = resolveConfig({
			env: { ANTHROPIC_API_KEY: "sk-ant-env" },
			credentials,
		});

		expect(config.source).toBe("byok");
		expect(config.model.provider).toBe("openrouter");
		expect(config.apiKey).toBe("sk-or-byok");
	});
});
