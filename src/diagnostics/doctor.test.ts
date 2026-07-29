import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CredentialsStore } from "../auth/credentials.js";
import { buildDoctorReport } from "./doctor.js";

describe("buildDoctorReport", () => {
	let root: string;
	let cwd: string;
	let dataRoot: string;

	beforeEach(() => {
		root = mkdtempSync(join(tmpdir(), "doctor-"));
		cwd = join(root, "project");
		dataRoot = join(root, ".codebase");
		mkdirSync(cwd);
	});

	afterEach(() => {
		rmSync(root, { recursive: true, force: true });
	});

	it("reports useful setup hints without requiring an agent session", () => {
		const out = buildDoctorReport({
			cwd,
			dataRoot,
			env: {},
			version: "test-version",
			nodeVersion: "20.1.0",
		}).join("\n");

		expect(out).toContain("codebase test-version · doctor");
		expect(out).toContain("✓ node 20.1.0");
		expect(out).toContain("– not signed in");
		expect(out).toContain("fix: codebase auth login");
		expect(out).toContain("model: resolved when a session starts");
		expect(out).toContain(`${dataRoot} writable`);
	});

	it("reports credentials, config parse failures, MCP status, and session metadata", () => {
		mkdirSync(dataRoot);
		mkdirSync(join(cwd, ".codebase"));
		writeFileSync(join(cwd, ".codebase", "config.json"), "{nope");
		new CredentialsStore({ dataRoot }).save({
			accessToken: "token",
			scopes: ["inference", "projects", "credits", "builds:read", "builds:write"],
			source: "codebase",
			expiresAt: Date.now() + 3_600_000,
		});

		const out = buildDoctorReport({
			cwd,
			dataRoot,
			env: {},
			model: { provider: "codebase", id: "d4f", name: "Codebase Auto" },
			source: "proxy",
			mcpStatuses: [{ name: "db", connected: false, toolCount: 0, error: "spawn failed" }],
			sessionCount: 3,
			subagentTypes: [{ name: "general" }],
		}).join("\n");

		expect(out).toContain("✓ signed in (codebase)");
		expect(out).toContain("✓ web build OAuth scopes present");
		expect(out).toContain("model: Codebase Auto (codebase/d4f) via proxy");
		expect(out).toContain("config ");
		expect(out).toContain("is not valid JSON");
		expect(out).toContain("mcp db: spawn failed");
		expect(out).toContain("✓ web_search configured (Codebase hosted)");
		expect(out).toContain("sessions for this directory: 3");
		expect(out).toContain("subagent types: general");
	});

	it("reports missing web build scopes on older OAuth tokens", () => {
		new CredentialsStore({ dataRoot }).save({
			accessToken: "token",
			scopes: ["inference", "projects", "credits"],
			source: "codebase",
			expiresAt: Date.now() + 3_600_000,
		});

		const out = buildDoctorReport({
			cwd,
			dataRoot,
			env: {},
			version: "test-version",
			nodeVersion: "20.1.0",
		}).join("\n");

		expect(out).toContain("✗ web build missing build scopes: builds:read builds:write");
		expect(out).toContain("fix: run `codebase auth login`");
	});
});
