#!/usr/bin/env node
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render } from "ink";
import { runAcpServer } from "./acp/server.js";
import { runAppServer } from "./app-server/server.js";
import { runAuthSubcommand } from "./auth/cli.js";
import { ensureFreshCredentials } from "./auth/ensure-fresh.js";
import { permissions as permissionsCommand } from "./commands/builtins/permissions.js";
import { fetchUsageReport } from "./commands/builtins/usage.js";
import type { CommandContext } from "./commands/types.js";
import { buildDoctorReport } from "./diagnostics/doctor.js";
import { runDirectorSubcommand } from "./directors/cli.js";
import { loadDotEnv } from "./dotenv/loader.js";
import { runReceiptSubcommand } from "./headless/receipt-cli.js";
import { type HeadlessOutputFormat, runHeadless } from "./headless/run.js";
import { PermissionStore } from "./permissions/store.js";
import { runProjectSubcommand } from "./projects/cli.js";
import { runSshSubcommand } from "./ssh/cli.js";
import { App } from "./ui/App.js";
import { installTerminalRestoreHandlers } from "./ui/terminal-restore.js";
import { setTerminalTitle } from "./ui/terminal-title.js";
import { VERSION } from "./version.js";

// Auto-load .env files before any subsystem reads process.env.
loadDotEnv();

const CLI_MODULE_DIR = dirname(fileURLToPath(import.meta.url));
const PACKAGE_ROOT = dirname(CLI_MODULE_DIR);

// Module-level consts referenced by `parseRunArgs`. Declared BEFORE
// the dispatch block below — `const` lives in the temporal dead zone
// until its declaration runs, so the dispatch can't reach `parseRunArgs`
// → `VALID_OUTPUT_FORMATS` until both have initialized.
interface ParsedRunArgs {
	prompt?: string;
	outputFormat?: HeadlessOutputFormat;
	autoApprove?: boolean;
	reliable?: boolean;
	maxTurns?: number;
	error?: string;
}

const VALID_OUTPUT_FORMATS = new Set<HeadlessOutputFormat>(["text", "json", "stream-json"]);

const rawArgv = process.argv.slice(2);

// Strip flags consumed at the top-level dispatcher before subcommand
// matching. `--debug-input` is one of those: it sets an env var that
// the Input component picks up, then disappears from argv so it can't
// confuse downstream parsers.
const argv: string[] = [];
for (const a of rawArgv) {
	if (a === "--debug-input") {
		process.env.CODEBASE_DEBUG_INPUT = "1";
		continue;
	}
	if (a === "--new" || a === "--fresh") {
		// Skip the auto-resume that the interactive TUI does by default —
		// useful when the prior session is no longer relevant or after a
		// destructive change to the working tree.
		process.env.CODEBASE_FRESH = "1";
		continue;
	}
	if (a === "--unrestricted" || a === "--yolo") {
		// Power-user mode: drops every soft-guard restriction. Equivalent
		// to setting CODEBASE_NO_PROJECT_ROOT=1 + CODEBASE_NO_VALIDATOR=1
		// + CODEBASE_NO_READ_BEFORE_WRITE=1. The agent can then read/write
		// anywhere, run any shell command, and overwrite files without
		// reading them first. Use when you trust the model + the prompt
		// (e.g. your own machine, your own project). The warning banner
		// at session start enumerates what's off so it's never accidental.
		process.env.CODEBASE_NO_PROJECT_ROOT = "1";
		process.env.CODEBASE_NO_VALIDATOR = "1";
		process.env.CODEBASE_NO_READ_BEFORE_WRITE = "1";
		continue;
	}
	argv.push(a);
}

if (argv[0] === "--version" || argv[0] === "-v") {
	process.stdout.write(`${VERSION}\n`);
	process.exit(0);
} else if (argv[0] === "--help" || argv[0] === "-h") {
	printHelp();
	process.exit(0);
} else if (argv[0] === "help") {
	const topic = argv[1];
	if (!topic || topic === "--help" || topic === "-h") {
		printHelp();
		printHelpTopics();
		process.exit(0);
	}
	if (printTopicHelp(topic)) process.exit(0);
	process.stderr.write(`unknown help topic: ${topic}\nRun \`codebase help\` to list topics.\n`);
	process.exit(2);
} else if (argv[0] === "permissions" || argv[0] === "allowed-tools") {
	process.exit(runPermissionsSubcommand(argv.slice(1)));
} else if (isHelpTopicShim(argv[0])) {
	printTopicHelp(argv[0]);
	process.exit(0);
} else if (argv[0] === "auth") {
	runAuthSubcommand(argv).then((code) => process.exit(code));
} else if (argv[0] === "ssh") {
	runSshSubcommand(argv).then((code) => process.exit(code));
} else if (argv[0] === "project" || argv[0] === "projects") {
	runProjectSubcommand(argv).then((code) => process.exit(code));
} else if (argv[0] === "web-build") {
	runProjectSubcommand(["project", "build", ...argv.slice(1)]).then((code) => process.exit(code));
} else if (argv[0] === "usage") {
	if (argv[1] === "--help" || argv[1] === "-h") {
		process.stdout.write("usage: codebase usage\n\nShow Codebase plan credits, reset date, and build turns.\n");
		process.exit(0);
	}
	fetchUsageReport().then((report) => {
		process.stdout.write(`${report}\n`);
		process.exit(0);
	});
} else if (argv[0] === "doctor") {
	if (argv[1] === "--help" || argv[1] === "-h") {
		process.stdout.write("usage: codebase doctor\n\nDiagnose local runtime, auth, config, MCP, and storage.\n");
		process.exit(0);
	}
	process.stdout.write(`${buildDoctorReport({ cwd: process.cwd() }).join("\n")}\n`);
	process.exit(0);
} else if (argv[0] === "mcp") {
	printMcpHelp();
	process.exit(0);
} else if (argv[0] === "director" || argv[0] === "directors") {
	runDirectorSubcommand(argv).then((code) => process.exit(code));
} else if (argv[0] === "receipt" || argv[0] === "receipts") {
	runReceiptSubcommand(argv).then((code) => process.exit(code));
} else if (argv[0] === "bench") {
	runBenchSubcommand(argv).then((code) => process.exit(code));
} else if (argv[0] === "app-server") {
	if (argv.slice(1).some((a) => a === "--help" || a === "-h")) {
		printAppServerHelp();
		process.exit(0);
	}
	// JSON-RPC-ish over stdio for IDE extensions. Auto-approve permissions
	// by default — IDE clients render approval UIs themselves and we don't
	// want the server to hang waiting on a TUI prompt no one's watching.
	// The `--no-auto-approve` flag is for clients that DO implement their
	// own approval flow via the `permission_request` event.
	const noAutoApprove = argv.includes("--no-auto-approve");
	const resume = argv.includes("--resume");
	// Refresh the saved access token if it's expired since the last
	// launch (proxy session, valid refresh token sitting next to it).
	// Otherwise createAgent would synchronously bail at "no usable
	// provider" and the IDE would see a setup_error envelope instead
	// of a working server.
	await ensureFreshCredentials();
	runAppServer({ autoApprove: !noAutoApprove, resume }).then((code) => process.exit(code));
} else if (argv[0] === "acp") {
	if (argv.slice(1).some((a) => a === "--help" || a === "-h")) {
		printAcpHelp();
		process.exit(0);
	}
	await ensureFreshCredentials();
	runAcpServer().then((code) => process.exit(code));
} else if (argv[0] === "run") {
	if (argv.slice(1).some((a) => a === "--help" || a === "-h")) {
		printRunHelp();
		process.exit(0);
	}
	const { prompt, outputFormat, autoApprove, reliable, maxTurns, error } = parseRunArgs(argv.slice(1));
	if (error) {
		process.stderr.write(`${error}\n`);
		process.exit(2);
	}
	if (!prompt) {
		process.stderr.write(
			"usage: codebase run [--output text|json|stream-json] [--auto-approve] [--reliable] [--max-turns n] <prompt>\n",
		);
		process.exit(2);
	}
	await ensureFreshCredentials();
	settleExitCode(runHeadless({ prompt, outputFormat, autoApprove, reliable, maxTurns }));
} else if (argv[0] === "auto") {
	if (argv.slice(1).some((a) => a === "--help" || a === "-h")) {
		printAutoHelp();
		process.exit(0);
	}
	const { prompt, outputFormat, reliable, maxTurns, error } = parseRunArgs(argv.slice(1));
	if (error) {
		process.stderr.write(`${error}\n`);
		process.exit(2);
	}
	if (!prompt) {
		process.stderr.write(
			"usage: codebase auto [--output text|json|stream-json] [--reliable] [--max-turns n] <prompt>\n",
		);
		process.exit(2);
	}
	await ensureFreshCredentials();
	settleExitCode(runHeadless({ prompt, outputFormat, autoApprove: true, reliable, maxTurns }));
} else {
	setTerminalTitle("codebase");
	// Print a one-line warning if any restriction is off so the user can't
	// accidentally launch a session in unrestricted mode without realizing.
	// Written before ink takes over the screen so it appears once at the
	// top, then scrolls away as normal output replaces it.
	printUnrestrictedBanner();
	// Enable bracketed paste mode so the terminal wraps pasted content in
	// CSI 200~ / 201~ markers. terminal-restore.ts emits the matching
	// disable sequence on every exit path.
	if (process.stdout.isTTY) process.stdout.write("\x1b[?2004h");
	// Cold-start credential refresh. If this is a returning user whose
	// saved access token expired while the laptop was closed, refresh
	// it now using the long-lived refresh token instead of dumping them
	// back to the login wizard. A network failure here is silent — the
	// wizard path catches it downstream.
	await ensureFreshCredentials();
	// pi-tui (differential renderer, no React) is the default. It has the
	// full feature set plus click-free copy mode, dynamic title, and
	// smoother streaming on long transcripts. CODEBASE_REACT_TUI=1 falls
	// back to the legacy ink path during the deprecation window.
	if (process.env.CODEBASE_REACT_TUI !== "1") {
		const { runPiTuiApp } = await import("./ui-pi/runtime.js");
		installTerminalRestoreHandlers();
		await runPiTuiApp();
		process.exit(0);
	}
	// Legacy ink/React path. Disable ink's default ctrl-c handling — ink
	// unmounts on ctrl-c but doesn't exit the process — leaves the user
	// staring at a frozen terminal. We handle ctrl-c ourselves.
	const instance = render(<App />, { exitOnCtrlC: false });
	installTerminalRestoreHandlers(instance);
	instance.waitUntilExit().catch(() => {
		process.exit(1);
	});
}

function settleExitCode(run: Promise<number>): void {
	run.then(
		(code) => {
			process.exitCode = code;
		},
		(err) => {
			process.stderr.write(`error: ${err instanceof Error ? err.message : String(err)}\n`);
			process.exitCode = 1;
		},
	);
}

function runPermissionsSubcommand(args: string[]): number {
	if (args.length === 0 || args.some((a) => a === "--help" || a === "-h")) {
		printPermissionsHelp();
		return 0;
	}

	const emitted: string[] = [];
	const ctx = {
		emit: (text: string) => emitted.push(text),
		bundle: {
			toolContext: { cwd: process.cwd() },
			permissions: new PermissionStore(),
		},
	} as unknown as CommandContext;

	permissionsCommand.handler(args.join(" "), ctx);
	if (emitted.length > 0) process.stdout.write(`${emitted.join("\n")}\n`);
	return 0;
}

function parseRunArgs(args: string[]): ParsedRunArgs {
	const remaining: string[] = [];
	let outputFormat: HeadlessOutputFormat | undefined;
	let autoApprove = false;
	let reliable = false;
	let maxTurns: number | undefined;
	for (let i = 0; i < args.length; i++) {
		const a = args[i];
		if (a === "--output" || a === "-o") {
			const value = args[i + 1];
			if (!value || !VALID_OUTPUT_FORMATS.has(value as HeadlessOutputFormat)) {
				return { error: `--output must be one of: ${[...VALID_OUTPUT_FORMATS].join(", ")}` };
			}
			outputFormat = value as HeadlessOutputFormat;
			i++;
			continue;
		}
		if (a.startsWith("--output=")) {
			const value = a.slice("--output=".length);
			if (!VALID_OUTPUT_FORMATS.has(value as HeadlessOutputFormat)) {
				return { error: `--output must be one of: ${[...VALID_OUTPUT_FORMATS].join(", ")}` };
			}
			outputFormat = value as HeadlessOutputFormat;
			continue;
		}
		if (a === "--auto-approve" || a === "--yes" || a === "-y") {
			autoApprove = true;
			continue;
		}
		if (a === "--reliable") {
			reliable = true;
			continue;
		}
		if (a === "--max-turns" || a.startsWith("--max-turns=")) {
			const value = a === "--max-turns" ? args[++i] : a.slice("--max-turns=".length);
			const parsed = Number(value);
			if (!Number.isInteger(parsed) || parsed < 1 || parsed > 200) {
				return { error: "--max-turns must be an integer from 1 to 200" };
			}
			maxTurns = parsed;
			continue;
		}
		remaining.push(a);
	}
	const prompt = remaining.join(" ").trim();
	return { prompt: prompt || undefined, outputFormat, autoApprove, reliable, maxTurns };
}

function printUnrestrictedBanner(): void {
	const off: string[] = [];
	if (process.env.CODEBASE_NO_PROJECT_ROOT === "1") off.push("project-root clamp");
	if (process.env.CODEBASE_NO_VALIDATOR === "1") off.push("shell validator");
	if (process.env.CODEBASE_NO_READ_BEFORE_WRITE === "1") off.push("read-before-write");
	if (off.length === 0) return;
	if (!process.stdout.isTTY) return;
	// Yellow background, black text — visible without being scary-red.
	const banner = `\x1b[43;30m⚠ UNRESTRICTED MODE — ${off.join(" + ")} disabled\x1b[0m`;
	process.stdout.write(`${banner}\n`);
}

function printHelp(): void {
	process.stdout.write(
		[
			"codebase — AI coding agent in your terminal",
			"",
			"Usage:",
			"  codebase                     run the interactive TUI in the current directory",
			"  codebase run --auto-approve <prompt>",
			"                               one-shot headless run, prints to stdout",
			"  codebase run --auto-approve --output json|stream-json <prompt>",
			"                               one-shot run with structured output",
			"  codebase run --auto-approve --reliable <prompt>",
			"                               require tasks, verification, and a receipt",
			"  codebase auto <prompt>       shortcut for run --auto-approve",
			"                               one-shot build/change in a trusted workspace",
			"  codebase receipt             inspect the latest reliable-mode receipt",
			"  codebase receipt list        list saved reliable-mode receipts",
			"  codebase bench run --scenario all --reliable true",
			"                               run reproducible agent benchmarks",
			"  codebase bench report <sweep-id>",
			"                               render benchmark scorecards",
			"  codebase help <topic>        show CLI or TUI feature help",
			"  codebase auth login          sign in via codebase.design browser OAuth",
			"  codebase auth logout         revoke the current session",
			"  codebase auth status         show current sign-in",
			"  codebase auth refresh        force-refresh the access token",
			"  codebase auth <token>        save a Codebase bearer token (advanced)",
			"  codebase usage               show Codebase plan credits and build turns",
			"  codebase ssh add <name> <host>    enroll a remote machine the agent can target",
			"  codebase ssh list / rm / test     manage enrolled SSH hosts",
			"  codebase ssh keygen <name>        generate an Ed25519 (or --rsa) keypair",
			"  codebase project list        list your projects on codebase.design",
			"  codebase project pull <id>   download a project as a ZIP",
			"  codebase project build <prompt>",
			"                               start a web build on codebase.design",
			"  codebase web-build <prompt>  shortcut for project build",
			"  codebase doctor              diagnose runtime, auth, config, MCP, storage",
			"  codebase mcp                 show MCP setup help",
			"  codebase memory              show memory help (TUI: /memory, #note)",
			"  codebase permissions         preview or configure shell permission rules",
			"  codebase agents              show subagent help (TUI: /agents)",
			"  codebase skills              show skill help (TUI: /skills)",
			"  codebase tournament          show tournament help (TUI: /tournament)",
			"  codebase director list       manage trained directors (hire, status, fire)",
			"  codebase acp                 Agent Client Protocol server on stdio",
			"  codebase app-server          JSON-RPC server on stdio (for IDE extensions)",
			"  codebase --version           print version and exit",
			"  codebase --help              show this message",
			"",
			"Session:",
			"  codebase                     resume the prior session for this directory if recent (≤7d)",
			"  codebase --new               start a fresh session, ignoring saved history",
			"  codebase --unrestricted      drop the project-root clamp, shell validator, and",
			"                               read-before-write check. Trust mode for your own machine.",
			"",
			"Diagnostics:",
			"  --debug-input                log every keystroke to ~/.codebase/logs/input.log",
			"                               (use when reporting a keyboard/terminal issue)",
			"",
			"More: https://github.com/codebase/codebase-cli",
			"",
		].join("\n"),
	);
}

function isHelpTopicShim(topic: string | undefined): boolean {
	if (!topic) return false;
	return [
		"memory",
		"permissions",
		"allowed-tools",
		"agents",
		"skills",
		"tournament",
		"race",
		"context",
		"model",
		"models",
		"effort",
		"rewind",
	].includes(topic);
}

function printHelpTopics(): void {
	process.stdout.write(
		[
			"Help topics:",
			"  auth, run, auto, project, web-build, ssh, usage, doctor, mcp, receipt, bench, director, acp, app-server",
			"  memory, permissions, agents, skills, tournament, context, model, effort, rewind",
			"",
			"Examples:",
			"  codebase help permissions",
			"  codebase permissions",
			"  codebase help web-build",
			"",
		].join("\n"),
	);
}

function printTopicHelp(rawTopic: string): boolean {
	const topic = rawTopic.replace(/^\/+/, "").trim().toLowerCase();
	switch (topic) {
		case "run":
			printRunHelp();
			return true;
		case "auto":
			printAutoHelp();
			return true;
		case "mcp":
			printMcpHelp();
			return true;
		case "usage":
			process.stdout.write("usage: codebase usage\n\nShow Codebase plan credits, reset date, and build turns.\n");
			return true;
		case "doctor":
			process.stdout.write("usage: codebase doctor\n\nDiagnose local runtime, auth, config, MCP, and storage.\n");
			return true;
		case "app-server":
			printAppServerHelp();
			return true;
		case "acp":
			printAcpHelp();
			return true;
		case "web-build":
			printWebBuildHelp();
			return true;
		case "project":
		case "projects":
			printProjectHelp();
			return true;
		case "auth":
			printAuthHelpSummary();
			return true;
		case "ssh":
			printSshHelpSummary();
			return true;
		case "receipt":
		case "receipts":
			printReceiptHelpSummary();
			return true;
		case "bench":
			printBenchHelp();
			return true;
		case "director":
		case "directors":
			printDirectorHelpSummary();
			return true;
		case "memory":
			printMemoryHelp();
			return true;
		case "permissions":
		case "allowed-tools":
			printPermissionsHelp();
			return true;
		case "agents":
		case "subagents":
			printAgentsHelp();
			return true;
		case "skills":
			printSkillsHelp();
			return true;
		case "tournament":
		case "race":
			printTournamentHelp();
			return true;
		case "context":
			printContextHelp();
			return true;
		case "model":
		case "models":
			printModelHelp();
			return true;
		case "effort":
			printEffortHelp();
			return true;
		case "rewind":
			printRewindHelp();
			return true;
		default:
			return false;
	}
}

function printMcpHelp(): void {
	process.stdout.write(
		[
			"usage: codebase mcp",
			"",
			"Configure MCP servers for codebase.",
			"",
			"Config files:",
			"  ~/.codebase/mcp.json",
			"  <project>/.codebase/mcp.json",
			"",
			"Example:",
			"  {",
			'    "mcpServers": {',
			'      "filesystem": {',
			'        "command": "npx",',
			'        "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"]',
			"      },",
			'      "remote": {',
			'        "url": "https://mcp.example.com/sse",',
			'        "headers": { "Authorization": "Bearer <token>" }',
			"      }",
			"    }",
			"  }",
			"",
			"Restart codebase after editing config. Inside the TUI, run /mcp to see",
			"connected servers, tools, resources, and prompts.",
			"",
		].join("\n"),
	);
}

function printAppServerHelp(): void {
	process.stdout.write(
		[
			"usage: codebase app-server [--resume] [--no-auto-approve]",
			"",
			"Run the JSONL app/IDE bridge on stdin/stdout.",
			"",
			"Protocol:",
			"  initialize, prompt, abort, get_state, get_messages, code_navigation, set_model, permission_respond",
			"  code_navigation supports definition, hover, symbols, references, implementation, diagnostics",
			"",
			"Events:",
			"  server_ready, agent events, permission_request, permission_cleared, usage_update, server_error",
			"",
			"Options:",
			"  --resume             resume the previous session for this directory",
			"  --no-auto-approve    emit permission_request events instead of auto-approving",
			"  --help, -h           show this help",
			"",
		].join("\n"),
	);
}

function printAcpHelp(): void {
	process.stdout.write(
		[
			"usage: codebase acp",
			"",
			"Run Codebase as an Agent Client Protocol (ACP) agent over stdio.",
			"",
			"ACP clients can create isolated sessions, stream assistant and tool updates,",
			"cancel turns, answer permission requests, and inject stdio or HTTP MCP servers.",
			"",
			"Buzz custom harness:",
			"  command: codebase",
			"  args: acp",
			"",
		].join("\n"),
	);
}

function printProjectHelp(): void {
	process.stdout.write(
		[
			"usage: codebase project [list|pull|build|status|preview|cancel] [options]",
			"",
			"Work with projects and web builds on codebase.design.",
			"",
			"Common commands:",
			"  codebase project list",
			"  codebase project pull <id>",
			"  codebase project build [--wait] [--model MODEL] [--scaffold ID] [--project ID] <prompt>",
			"  codebase web-build [--wait] [--model MODEL] [--scaffold ID] [--project ID] <prompt>",
			"",
			"Run `codebase project --help` or `codebase project build --help` for full project help.",
			"",
		].join("\n"),
	);
}

function printWebBuildHelp(): void {
	process.stdout.write(
		[
			"usage: codebase web-build [--wait] [--model MODEL] [--scaffold ID] [--project ID] <prompt>",
			"",
			"Shortcut for `codebase project build`: start a web build on codebase.design.",
			"",
			"Options:",
			"  --wait               stream build events until completion",
			"  --model MODEL        request a specific model id",
			"  --scaffold ID        start from a saved scaffold/template",
			"  --project ID         continue an existing project",
			"  --help, -h           show this help",
			"",
		].join("\n"),
	);
}

function printAuthHelpSummary(): void {
	process.stdout.write(
		[
			"usage: codebase auth [login|status|refresh|logout|<token>]",
			"",
			"Manage Codebase OAuth or pasted bearer-token credentials.",
			"",
			"Common commands:",
			"  codebase auth login",
			"  codebase auth status",
			"  codebase auth refresh",
			"  codebase auth logout",
			"",
			"Run `codebase auth --help` for full auth help.",
			"",
		].join("\n"),
	);
}

function printSshHelpSummary(): void {
	process.stdout.write(
		[
			"usage: codebase ssh [add|list|rm|test|keygen] ...",
			"",
			"Manage enrolled SSH hosts for remote tool execution.",
			"",
			"Run `codebase ssh --help` for full SSH help.",
			"",
		].join("\n"),
	);
}

function printReceiptHelpSummary(): void {
	process.stdout.write(
		[
			"usage: codebase receipt [list | show [id] | export [id]] [--json|--markdown] [--out path]",
			"",
			"Inspect reliable-mode receipts saved by `codebase auto --reliable`.",
			"",
			"Run `codebase receipt --help` for full receipt help.",
			"",
		].join("\n"),
	);
}

function printBenchHelp(): void {
	process.stdout.write(
		[
			"usage: codebase bench <run|report> [options]",
			"",
			"Run reproducible end-to-end CLI agent benchmarks and generate public scorecards.",
			"",
			"Common commands:",
			"  codebase bench run --scenario fix-typo",
			"  codebase bench run --scenario all --runs 3 --reliable true",
			"  codebase bench report <sweep-id>",
			"  codebase bench report <sweep-id> --out docs/benchmarks/<id>.md --json-out docs/benchmarks/<id>.json",
			"",
			"Run options:",
			"  --scenario NAME|all     scenario to run (default: all)",
			"  --runs N                runs per scenario (default: 1)",
			"  --reliable true         require reliable-mode receipts",
			"  --cli PATH              benchmark a specific codebase binary",
			"  --model MODEL           request a specific model id",
			"  --sweep-id ID           stable results id under ./bench/results/",
			"  --isolate-home false    use your real HOME instead of a copied temp HOME",
			"  --keep-tmp true         keep temporary projects for inspection",
			"",
			"Before running real sweeps, build once and sign in or provide an API key:",
			"  npm run build",
			"  codebase auth login",
			"",
			"More:",
			"  codebase bench run --help",
			"  codebase bench report --help",
			"",
		].join("\n"),
	);
}

async function runBenchSubcommand(args: string[]): Promise<number> {
	const subcommand = args[1];
	if (!subcommand || subcommand === "--help" || subcommand === "-h" || subcommand === "help") {
		printBenchHelp();
		return 0;
	}
	if (subcommand === "run") {
		return spawnBenchScript("run.mjs", args.slice(2));
	}
	if (subcommand === "report" || subcommand === "aggregate") {
		return spawnBenchScript("aggregate.mjs", args.slice(2));
	}
	process.stderr.write(`unknown bench command: ${subcommand}\nRun \`codebase bench --help\`.\n`);
	return 2;
}

function spawnBenchScript(scriptName: string, args: string[]): Promise<number> {
	const scriptPath = join(PACKAGE_ROOT, "bench", scriptName);
	if (!existsSync(scriptPath)) {
		process.stderr.write(
			[
				`benchmark harness is not present at ${scriptPath}`,
				"Reinstall codebase-cli or run benchmarks from a source checkout.",
				"",
			].join("\n"),
		);
		return Promise.resolve(1);
	}
	return new Promise((resolveRun) => {
		const env = {
			...process.env,
			CODEBASE_BENCH_RESULTS_DIR: process.env.CODEBASE_BENCH_RESULTS_DIR ?? join(process.cwd(), "bench", "results"),
		};
		const child = spawn(process.execPath, [scriptPath, ...args], {
			cwd: process.cwd(),
			env,
			stdio: "inherit",
		});
		child.on("error", (err) => {
			process.stderr.write(`error: failed to launch benchmark harness: ${err.message}\n`);
			resolveRun(1);
		});
		child.on("close", (code) => {
			resolveRun(code ?? 1);
		});
	});
}

function printDirectorHelpSummary(): void {
	process.stdout.write(
		[
			"usage: codebase director [list|hire|status|fire] ...",
			"",
			"Manage trained design directors used by web-build prompts.",
			"",
			"Run `codebase director --help` for full director help.",
			"",
		].join("\n"),
	);
}

function printMemoryHelp(): void {
	process.stdout.write(
		[
			"usage: codebase memory",
			"",
			"Show memory help for the interactive TUI.",
			"",
			"Inside `codebase`:",
			"  /memory              show this project's MEMORY.md index",
			"  #note text           save a quick memory without spending an agent turn",
			"  save_memory          tool the agent uses for durable project/user facts",
			"  read_memory          tool the agent uses to read saved memory bodies",
			"",
			"Storage:",
			"  ~/.codebase/projects/<project-hash>/memory/",
			"",
		].join("\n"),
	);
}

function printPermissionsHelp(): void {
	process.stdout.write(
		[
			"usage: codebase permissions [shell|suggest|simulate|allow|deny|remove] ...",
			"",
			"Preview or configure tool-permission rules.",
			"",
			"From your shell:",
			'  codebase permissions suggest "npm install"',
			'  codebase permissions simulate "npm test && git status"',
			'  codebase permissions allow "shell:npm run build*"',
			"",
			"Inside `codebase`:",
			"  /permissions                         list effective allow/deny rules",
			"  /permissions shell                   explain shell auto-allow policy",
			"  /permissions suggest <command>       preview shell risk and trust scope",
			"  /permissions simulate <plan>         preview allow/prompt/block for shell commands",
			"  /permissions allow <pattern>         persist an allow rule",
			"  /permissions deny <pattern>          persist a deny rule",
			"  /permissions remove <pattern>        remove a user-layer rule",
			"",
			"Pattern examples:",
			"  shell:git status*",
			"  shell:npm run build*",
			"  read_file:src/**",
			"",
		].join("\n"),
	);
}

function printAgentsHelp(): void {
	process.stdout.write(
		[
			"usage: codebase agents",
			"",
			"Show subagent help for the interactive TUI.",
			"",
			"Inside `codebase`:",
			"  /agents              list available subagent types",
			"  dispatch_agent       tool for launching focused read-only or write-capable workers",
			"",
			"Custom agents:",
			"  ~/.codebase/agents/<name>.md",
			"  <project>/.codebase/agents/<name>.md",
			"",
			"Frontmatter supports: description, tools, model, effort, max_turns.",
			"",
		].join("\n"),
	);
}

function printSkillsHelp(): void {
	process.stdout.write(
		[
			"usage: codebase skills",
			"",
			"Show skill help for the interactive TUI.",
			"",
			"Inside `codebase`:",
			"  /skills              list loaded markdown skills",
			"  /<skill-id> args     invoke a skill as a slash command",
			"",
			"Skill locations:",
			"  ~/.codebase/skills/<id>.md",
			"  <project>/.codebase/skills/<id>.md",
			"",
		].join("\n"),
	);
}

function printTournamentHelp(): void {
	process.stdout.write(
		[
			"usage: codebase tournament",
			"",
			"Show tournament help for the interactive TUI.",
			"",
			"Inside `codebase`:",
			"  /tournament <task>                 race 3 agents in isolated worktrees",
			"  /tournament 5 <task>               choose contestant count (max 5)",
			"  /tournament --models a,b,c <task>  race specific model ids",
			"",
			"The pi-tui merge picker lets you inspect contestants and merge the winner.",
			"",
		].join("\n"),
	);
}

function printContextHelp(): void {
	process.stdout.write(
		[
			"usage: codebase context",
			"",
			"Show context-visibility help for the interactive TUI.",
			"",
			"Inside `codebase`:",
			"  /context             summarize model context pressure, tasks, memory, tools, and compaction",
			"  /context explain     show recent/largest messages, memory matches, and compaction details",
			"",
			"Launch smoke:",
			"  npm run build && npm run smoke:context",
			"",
		].join("\n"),
	);
}

function printModelHelp(): void {
	process.stdout.write(
		[
			"usage: codebase help model",
			"",
			"Inside `codebase`:",
			"  /model               open the model picker",
			"  /model <id>          switch to a model id",
			"  /models              list available models when the provider supports it",
			"",
			"OAuth users route models through Codebase Auto/codebase.design unless BYOK is configured.",
			"",
		].join("\n"),
	);
}

function printEffortHelp(): void {
	process.stdout.write(
		[
			"usage: codebase help effort",
			"",
			"Inside `codebase`:",
			"  /effort              show current reasoning effort",
			"  /effort low|medium|high|xhigh",
			"",
		].join("\n"),
	);
}

function printRewindHelp(): void {
	process.stdout.write(
		[
			"usage: codebase help rewind",
			"",
			"Inside `codebase`:",
			"  /rewind              open the timeline/checkpoint picker when available",
			"  /rewind list         list file checkpoints",
			"  /rewind <seq>        restore one checkpoint",
			"",
		].join("\n"),
	);
}

function printRunHelp(): void {
	process.stdout.write(
		[
			"usage: codebase run [--output text|json|stream-json] [--auto-approve] [--reliable] [--max-turns n] <prompt>",
			"",
			"Run one non-interactive agent turn and print the result to stdout.",
			"",
			"Options:",
			"  --output, -o text|json|stream-json   choose stdout format (default: text)",
			"  --auto-approve, --yes, -y            required: allow tool calls without interactive prompts",
			"  --reliable                           fail without completed tasks + verification receipt",
			"  --max-turns n                         stop runaway tool loops after n turns (default: 40)",
			"  --help, -h                           show this help",
			"",
			"Shortcut:",
			"  codebase auto <prompt>               same as run --auto-approve <prompt>",
			"",
		].join("\n"),
	);
}

function printAutoHelp(): void {
	process.stdout.write(
		[
			"usage: codebase auto [--output text|json|stream-json] [--reliable] [--max-turns n] <prompt>",
			"",
			"Run one trusted, non-interactive coding task with tool calls auto-approved.",
			"",
			"Equivalent to:",
			"  codebase run --auto-approve <prompt>",
			"",
			"Options:",
			"  --output, -o text|json|stream-json   choose stdout format (default: text)",
			"  --reliable                           fail without completed tasks + verification receipt",
			"  --max-turns n                         stop runaway tool loops after n turns (default: 40)",
			"  --help, -h                           show this help",
			"",
		].join("\n"),
	);
}
