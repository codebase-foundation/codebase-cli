import { accessSync, constants, existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { CredentialsStore } from "../auth/credentials.js";
import { webBuildScopeReadiness } from "../auth/scopes.js";
import { VERSION } from "../version.js";

export interface DoctorReportOptions {
	cwd: string;
	dataRoot?: string;
	env?: NodeJS.ProcessEnv;
	version?: string;
	nodeVersion?: string;
	model?: { provider: string; id: string; name: string };
	source?: string;
	mcpStatuses?: readonly { name: string; connected: boolean; toolCount: number; error?: string }[];
	sessionCount?: number;
	subagentTypes?: readonly { name: string }[];
}

/**
 * Build the local health report used by both top-level `codebase doctor`
 * and interactive `/doctor`. Keep it read-only and safe to paste into
 * support tickets.
 */
export function buildDoctorReport(options: DoctorReportOptions): string[] {
	const env = options.env ?? process.env;
	const version = options.version ?? VERSION;
	const nodeVersion = options.nodeVersion ?? process.versions.node;
	const cwd = options.cwd;
	const dataRoot = options.dataRoot ?? join(homedir(), ".codebase");
	const lines: string[] = [`codebase ${version} · doctor`];

	const major = Number.parseInt(nodeVersion.split(".")[0] ?? "0", 10);
	lines.push(check(major >= 20, `node ${nodeVersion}`, "node ≥ 20 required"));

	const credStore = new CredentialsStore({ dataRoot });
	const creds = credStore.load();
	if (!creds) {
		lines.push(
			info(credStore.exists() ? "credentials file present but unreadable/invalid" : "not signed in"),
			info("fix: codebase auth login, codebase --new, or set an *_API_KEY env var"),
		);
	} else if (credStore.isExpired(creds)) {
		lines.push(
			check(
				false,
				"",
				`credentials expired${creds.refreshToken ? " (will auto-refresh on next call)" : " — run codebase auth login"}`,
			),
		);
	} else {
		const until = creds.expiresAt ? ` until ${new Date(creds.expiresAt).toLocaleString()}` : "";
		lines.push(check(true, `signed in (${creds.source})${until}`, ""));
		const webBuild = webBuildScopeReadiness(creds);
		if (webBuild.status === "ready") {
			lines.push(check(true, "web build OAuth scopes present", ""));
		} else if (webBuild.status === "missing-scopes") {
			lines.push(check(false, "", `web build ${webBuild.message}; fix: ${webBuild.fix}`));
		} else {
			lines.push(info(`web build: ${webBuild.message}; ${webBuild.fix}`));
		}
	}

	if (options.model) {
		lines.push(
			check(
				true,
				`model: ${options.model.name} (${options.model.provider}/${options.model.id}) via ${options.source}`,
				"",
			),
		);
	} else {
		lines.push(info("model: resolved when a session starts"));
	}

	for (const path of [join(dataRoot, "config.json"), join(cwd, ".codebase", "config.json")]) {
		if (!existsSync(path)) continue;
		lines.push(check(parses(path), `config ${path}`, `config ${path} is not valid JSON`));
	}
	for (const path of [join(dataRoot, "mcp.json"), join(cwd, ".codebase", "mcp.json")]) {
		if (!existsSync(path)) continue;
		lines.push(check(parses(path), `mcp config ${path}`, `mcp config ${path} is not valid JSON`));
	}

	for (const s of options.mcpStatuses ?? []) {
		lines.push(check(s.connected, `mcp ${s.name}: ${s.toolCount} tools`, `mcp ${s.name}: ${s.error ?? "failed"}`));
	}

	const localSearch = Boolean(env.TAVILY_API_KEY || env.BRAVE_API_KEY || env.SEARXNG_URL);
	const hostedSearch = Boolean(creds && creds.source !== "byok");
	lines.push(
		localSearch
			? check(true, "web_search configured (local provider)", "")
			: hostedSearch
				? check(true, "web_search configured (Codebase hosted)", "")
				: info(
						"web_search unconfigured — sign in with `codebase auth login` or set TAVILY_API_KEY, BRAVE_API_KEY, or SEARXNG_URL",
					),
	);

	lines.push(
		check(
			writable(dataRoot),
			`${dataRoot} writable`,
			`${dataRoot} is not writable — sessions/credentials can't persist`,
		),
	);

	if (options.sessionCount !== undefined) lines.push(info(`sessions for this directory: ${options.sessionCount}`));
	if (options.subagentTypes) {
		lines.push(info(`subagent types: ${options.subagentTypes.map((t) => t.name).join(", ") || "none"}`));
	}

	return lines;
}

function check(ok: boolean, okText: string, failText: string): string {
	return ok ? `  ✓ ${okText}` : `  ✗ ${failText}`;
}

function info(text: string): string {
	return `  – ${text}`;
}

function parses(path: string): boolean {
	try {
		JSON.parse(readFileSync(path, "utf8"));
		return true;
	} catch {
		return false;
	}
}

function writable(dir: string): boolean {
	try {
		accessSync(existsSync(dir) ? dir : dirname(dir), constants.W_OK);
		return true;
	} catch {
		return false;
	}
}
