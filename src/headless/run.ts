import type { AgentEvent, AgentMessage } from "@earendil-works/pi-agent-core";
import type { Usage } from "@earendil-works/pi-ai";
import { type AgentBundle, type CreateAgentOptions, createAgent } from "../agent/agent.js";
import { ConfigError } from "../agent/config.js";
import { visibleMessages } from "../agent/visible-messages.js";
import { latestAssistantError, userFacingErrorMessage } from "../errors/user-facing.js";
import { ReceiptStore } from "./receipt-store.js";
import {
	formatReliabilityFailure,
	formatReliabilityRepairPrompt,
	RELIABLE_MODE_PROMPT,
	type ReliabilityReceipt,
	ReliabilityRecorder,
} from "./reliable.js";

const EMPTY_USAGE: Usage = {
	input: 0,
	output: 0,
	cacheRead: 0,
	cacheWrite: 0,
	totalTokens: 0,
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};

export type HeadlessOutputFormat = "text" | "json" | "stream-json";

export interface HeadlessOptions {
	prompt: string;
	resume?: boolean;
	/** Maximum tool-using model turns before a runaway run is stopped. Default 40. */
	maxTurns?: number;
	/** Output shape. Default `text`. */
	outputFormat?: HeadlessOutputFormat;
	/**
	 * Auto-approve every permission prompt. Required for any non-
	 * interactive use — without it the agent hangs forever the first
	 * time a write tool fires, since there's no TUI to answer.
	 */
	autoApprove?: boolean;
	/**
	 * Reliable mode audits the run after the agent settles. The run must
	 * produce task state and successful verification evidence, and JSON
	 * output includes a machine-readable receipt.
	 */
	reliable?: boolean;
	stdout?: (chunk: string) => void;
	stderr?: (chunk: string) => void;
	/**
	 * Test escape hatch — passed straight through to createAgent so unit
	 * tests can inject a pi-ai faux provider instead of requiring real
	 * env-var keys. Production code never sets this.
	 */
	configOverride?: CreateAgentOptions["configOverride"];
}

/**
 * Run a single prompt without the TUI:
 *
 *   • text          (default) — assistant text to stdout, tool
 *                    activity to stderr, plain text. Pipe-friendly:
 *                    `codebase run "…" > out.md`.
 *   • stream-json   — every AgentEvent as a JSONL line on stdout, in
 *                    real time. Suitable for an upstream consumer
 *                    (CI, IDE integration) that wants progress events.
 *   • json          — buffer everything; on completion emit ONE JSON
 *                    object with the final transcript + usage + exit
 *                    metadata. Suitable for one-shot CI calls that
 *                    just want the final answer + cost.
 *
 * In stream-json/json modes errors land in stdout as a structured
 * event with `type: "error"` so consumers don't have to merge
 * stdout+stderr. Setup errors (e.g. ConfigError before the loop
 * starts) still go to stderr because they predate the JSON
 * envelope.
 */
export async function runHeadless(opts: HeadlessOptions): Promise<number> {
	const out = opts.stdout ?? ((c) => process.stdout.write(c));
	const err = opts.stderr ?? ((c) => process.stderr.write(c));
	const format: HeadlessOutputFormat = opts.outputFormat ?? "text";

	let bundle: AgentBundle;
	try {
		bundle = createAgent({
			resume: opts.resume,
			persistSession: opts.resume === true,
			autoApprove: opts.autoApprove,
			systemPromptAddendum: opts.reliable ? RELIABLE_MODE_PROMPT : undefined,
			configOverride: opts.configOverride,
		});
	} catch (e) {
		const msg = e instanceof ConfigError ? e.message : e instanceof Error ? e.message : String(e);
		const code = e instanceof ConfigError ? "config_error" : "setup_error";
		// stream-json / json consumers shouldn't have to merge stdout +
		// stderr to detect setup failures — emit a structured error in
		// the same envelope they expect for runtime errors. Text mode
		// still goes to stderr where ops consumers expect to see it.
		if (format === "stream-json") {
			out(`${JSON.stringify({ type: "error", code, error: msg, ts: Date.now() })}\n`);
		} else if (format === "json") {
			out(
				`${JSON.stringify({
					ok: false,
					exitCode: 1,
					error: msg,
					code,
					messages: [],
					usage: EMPTY_USAGE,
					model: null,
					source: null,
					durationMs: 0,
				})}\n`,
			);
		} else {
			err(`error: ${msg}\n`);
		}
		return 1;
	}

	if (!opts.autoApprove) {
		const msg =
			"Headless runs cannot answer tool approval prompts. Re-run with `--auto-approve` in a trusted workspace, or use interactive `codebase`.";
		if (format === "stream-json") {
			out(`${JSON.stringify({ type: "error", code: "approval_required", error: msg, ts: Date.now() })}\n`);
		} else if (format === "json") {
			out(
				`${JSON.stringify({
					ok: false,
					exitCode: 2,
					error: msg,
					code: "approval_required",
					messages: [],
					usage: EMPTY_USAGE,
					model: null,
					source: null,
					durationMs: 0,
				})}\n`,
			);
		} else {
			err(`error: ${msg}\n`);
		}
		return 2;
	}

	const startedAt = Date.now();
	let aborted = false;
	let errored = false;
	let errorCode: string | undefined;
	let errorMessage: string | undefined;
	let errorExitCode = 1;
	const maxTurns = Math.max(1, Math.min(200, opts.maxTurns ?? 40));
	let turns = 0;
	let turnLimitReached = false;
	let totalUsage: Usage = { ...EMPTY_USAGE, cost: { ...EMPTY_USAGE.cost } };
	const pendingTools = new Map<string, string>();
	const reliability = opts.reliable ? new ReliabilityRecorder() : null;

	// Connect configured MCP servers so headless runs (CI, scripts) can
	// use MCP tools too. Best-effort: a failed server is skipped, never
	// blocks the run. Subprocesses are torn down in the finally below.
	await bundle.connectMcp().catch(() => undefined);

	// Always tap the event stream for usage accumulation, regardless of
	// output format — pi-agent-core surfaces per-turn usage on
	// message_end events; the JSON output reports the running total.
	const usageUnsub = bundle.subscribe((event: AgentEvent) => {
		if (event.type !== "message_end") return;
		const candidate = (event.message as { usage?: Usage }).usage;
		if (candidate) totalUsage = mergeUsage(totalUsage, candidate);
	});
	const lifecycleUnsub = bundle.subscribe((event: AgentEvent) => {
		reliability?.record(event);
		if (event.type === "tool_execution_start") {
			const id = (event as { toolCallId?: string }).toolCallId;
			if (id) pendingTools.set(id, event.toolName);
		} else if (event.type === "tool_execution_end") {
			const id = (event as { toolCallId?: string }).toolCallId;
			if (id) pendingTools.delete(id);
		} else if (event.type === "turn_end" && event.message.role === "assistant") {
			turns++;
			const hasToolCalls =
				Array.isArray(event.message.content) && event.message.content.some((block) => block.type === "toolCall");
			if (hasToolCalls && turns >= maxTurns) {
				turnLimitReached = true;
				bundle.agent.abort();
			}
		}
	});

	const onSigInt = () => {
		aborted = true;
		bundle.agent.abort();
	};
	process.on("SIGINT", onSigInt);

	let unsubscribe: () => void;
	if (format === "stream-json") {
		unsubscribe = subscribeStreamJson(bundle, out);
	} else if (format === "json") {
		unsubscribe = () => {
			// JSON mode buffers via the agent's own state.messages; the
			// subscribe handler is a no-op so we don't spam stderr.
		};
	} else {
		unsubscribe = subscribeText(bundle, out, err);
	}

	try {
		// Route through the bundle helper so UserPromptSubmit hooks fire on
		// headless runs too (CI scripts, scheduled jobs). A hook veto exits
		// with code 1 and the reason printed to stderr.
		const submittedPrompt = opts.reliable ? buildReliableUserPrompt(opts.prompt) : opts.prompt;
		const submitResult = await bundle.submitUserPrompt(submittedPrompt);
		if (turnLimitReached) {
			errored = true;
			errorCode = "turn_limit_reached";
			errorMessage = `Agent stopped after ${maxTurns} turns to prevent a runaway tool loop. Re-run with --max-turns <n> only after reviewing the failure pattern.`;
			if (format === "stream-json") {
				out(`${JSON.stringify({ type: "error", code: errorCode, error: errorMessage, ts: Date.now() })}\n`);
			} else if (format !== "json") {
				err(`error: ${errorMessage}\n`);
			}
		} else if (!submitResult.submitted) {
			errored = true;
			errorCode = "prompt_blocked";
			errorMessage = submitResult.reason ?? "Prompt blocked by hook.";
			if (format === "stream-json") {
				out(`${JSON.stringify({ type: "error", code: "prompt_blocked", error: errorMessage, ts: Date.now() })}\n`);
			} else if (format !== "json") {
				err(`prompt blocked: ${errorMessage}\n`);
			}
			// json mode picks this up in buildJsonResult below via errorMessage.
		} else if (submitResult.error) {
			// Agent threw before the event stream emitted agent_end. Surface
			// the error explicitly so CI scripts see a non-zero exit + a real
			// error message instead of a "succeeded with empty output" run.
			errored = true;
			errorCode = "agent_error";
			errorMessage = userFacingErrorMessage(submitResult.error);
			if (format === "stream-json") {
				out(`${JSON.stringify({ type: "error", code: "agent_error", error: errorMessage, ts: Date.now() })}\n`);
			} else if (format !== "json") {
				err(`agent error: ${errorMessage}\n`);
			}
		}
		if (!errored && !aborted) {
			const turnError = latestAssistantError(bundle.agent.state.messages);
			if (turnError) {
				errored = true;
				errorCode = "agent_error";
				errorMessage = userFacingErrorMessage(turnError);
				if (format === "stream-json") {
					out(`${JSON.stringify({ type: "error", code: "agent_error", error: errorMessage, ts: Date.now() })}\n`);
				} else if (format !== "json") {
					err(`agent error: ${errorMessage}\n`);
				}
			}
		}
		if (!errored && !aborted && pendingTools.size > 0) {
			errored = true;
			const pending = [...pendingTools.values()].join(", ");
			if (opts.autoApprove) {
				errorCode = "agent_error";
				errorMessage = `Tool execution did not complete: ${pending}`;
			} else {
				errorCode = "approval_required";
				errorExitCode = 2;
				errorMessage =
					`Tool approval required for ${pending}, but headless mode has no approval UI. ` +
					"Re-run with `--auto-approve` in a trusted workspace, or use interactive `codebase`.";
			}
			if (format === "stream-json") {
				out(`${JSON.stringify({ type: "error", code: errorCode, error: errorMessage, ts: Date.now() })}\n`);
			} else if (format !== "json") {
				err(`agent error: ${errorMessage}\n`);
			}
		}
		if (!errored && !aborted && reliability) {
			const draftReceipt = reliability.build({
				tasks: bundle.toolContext.tasks.list(),
				checkpoints: bundle.checkpoints.list(),
				durationMs: Date.now() - startedAt,
				finalText: latestAssistantText(bundle.agent.state.messages),
			});
			if (!draftReceipt.ok) {
				try {
					await bundle.agent.prompt(formatReliabilityRepairPrompt(draftReceipt));
				} catch {
					if (format === "stream-json") {
						out(
							`${JSON.stringify({
								type: "reliability_repair_error",
								error: "Reliable-mode repair turn failed; falling back to the original receipt failure.",
								ts: Date.now(),
							})}\n`,
						);
					} else if (format !== "json") {
						err("[reliable repair failed] falling back to the original receipt failure\n");
					}
				}
			}
		}
	} catch (e) {
		errored = true;
		errorCode = "agent_error";
		errorMessage = userFacingErrorMessage(e instanceof Error ? e.message : String(e));
		if (format === "stream-json") {
			out(`${JSON.stringify({ type: "error", code: errorCode, error: errorMessage, ts: Date.now() })}\n`);
		} else if (format !== "json") {
			err(`agent error: ${errorMessage}\n`);
		}
	} finally {
		unsubscribe();
		usageUnsub();
		lifecycleUnsub();
		process.off("SIGINT", onSigInt);
		// Terminate MCP server subprocesses so a headless run doesn't leak
		// children to the parent shell / CI runner.
		bundle.mcp.dispose();
		bundle.checkpoints.dispose();
	}

	let receipt: ReliabilityReceipt | undefined;
	let receiptRecord: { id: string; path: string } | undefined;
	if (reliability) {
		receipt = reliability.build({
			tasks: bundle.toolContext.tasks.list(),
			checkpoints: bundle.checkpoints.list(),
			durationMs: Date.now() - startedAt,
			finalText: latestAssistantText(bundle.agent.state.messages),
		});
		if (format === "stream-json") {
			out(`${JSON.stringify({ type: "reliability_receipt", receipt, ts: Date.now() })}\n`);
		}
		if (!receipt.ok && !errored && !aborted) {
			errored = true;
			errorCode = "reliable_gate_failed";
			errorMessage = formatReliabilityFailure(receipt);
			if (format === "stream-json") {
				out(`${JSON.stringify({ type: "error", code: errorCode, error: errorMessage, ts: Date.now() })}\n`);
			} else if (format !== "json") {
				err(`error: ${errorMessage}\n`);
			}
		} else if (receipt.ok && format === "text") {
			err(
				`[reliable OK] ${receipt.summary.completedTasks}/${receipt.summary.taskCount} tasks complete; ` +
					`${receipt.summary.verificationCount} verification command${receipt.summary.verificationCount === 1 ? "" : "s"} passed.\n`,
			);
		}
	}

	const exitCode = aborted ? 130 : errored ? errorExitCode : 0;
	if (receipt) {
		try {
			const store = new ReceiptStore();
			const record = store.save({
				cwd: bundle.toolContext.cwd,
				prompt: opts.prompt,
				ok: !errored && !aborted,
				exitCode,
				error: errorMessage,
				code: errorCode,
				model: { provider: bundle.model.provider, id: bundle.model.id, name: bundle.model.name },
				source: bundle.source,
				durationMs: Date.now() - startedAt,
				usage: totalUsage,
				finalText: latestAssistantText(bundle.agent.state.messages),
				receipt,
			});
			receiptRecord = { id: record.id, path: store.pathFor(record.id) };
			if (format === "stream-json") {
				out(
					`${JSON.stringify({ type: "receipt_saved", id: receiptRecord.id, path: receiptRecord.path, ts: Date.now() })}\n`,
				);
			} else if (format === "text") {
				err(`[receipt] ${receiptRecord.path}\n`);
			}
		} catch (saveErr) {
			const saveMessage = saveErr instanceof Error ? saveErr.message : String(saveErr);
			if (format === "stream-json") {
				out(`${JSON.stringify({ type: "receipt_save_error", error: saveMessage, ts: Date.now() })}\n`);
			} else if (format !== "json") {
				err(`[receipt save failed] ${saveMessage}\n`);
			}
		}
	}

	if (format === "json") {
		const payload = buildJsonResult({
			ok: !errored && !aborted,
			exitCode,
			error: errorMessage,
			errorCode,
			messages: bundle.agent.state.messages,
			usage: totalUsage,
			model: { provider: bundle.model.provider, id: bundle.model.id, name: bundle.model.name },
			source: bundle.source,
			durationMs: Date.now() - startedAt,
			receipt,
			receiptRecord,
		});
		out(`${JSON.stringify(payload)}\n`);
	}

	return exitCode;
}

function buildReliableUserPrompt(prompt: string): string {
	return [
		"<system-reminder>",
		"Reliable mode is enabled for this run. The run will fail unless the transcript produces an auditable receipt:",
		"- create task entries before doing work, even for simple, read-only, or memory-only requests",
		"- set exactly one task to in_progress before using non-task tools for that task",
		"- complete or cancel every task before the final answer",
		"- for file/code changes, run a passing verification or smoke command after the last file change while a task is in_progress",
		"- do not make verification pass by hiding failures with `|| true`, `|| echo`, `|| :`, or `; echo $?`",
		"- for absence checks, use unmasked negation such as `! grep ... && grep ...`, or write a verify script",
		"- for file/code changes, name the exact fresh passing command in the final answer",
		"- for read-only or memory-only work, say no file-change verification was needed",
		"</system-reminder>",
		"",
		prompt,
	].join("\n");
}

function mergeUsage(a: Usage, b: Usage): Usage {
	return {
		input: a.input + b.input,
		output: a.output + b.output,
		cacheRead: a.cacheRead + b.cacheRead,
		cacheWrite: a.cacheWrite + b.cacheWrite,
		totalTokens: a.totalTokens + b.totalTokens,
		cost: {
			input: a.cost.input + b.cost.input,
			output: a.cost.output + b.cost.output,
			cacheRead: a.cost.cacheRead + b.cost.cacheRead,
			cacheWrite: a.cost.cacheWrite + b.cost.cacheWrite,
			total: a.cost.total + b.cost.total,
		},
	};
}

interface JsonResultInput {
	ok: boolean;
	exitCode: number;
	error?: string;
	errorCode?: string;
	messages: AgentMessage[];
	usage: unknown;
	model: { provider: string; id: string; name: string };
	source: string;
	durationMs: number;
	receipt?: ReliabilityReceipt;
	receiptRecord?: { id: string; path: string };
}

/** Exported for unit tests — production code reaches it through runHeadless. */
export function buildJsonResult(input: JsonResultInput): Record<string, unknown> {
	const messages = visibleMessages(input.messages);
	const lastAssistant = [...messages].reverse().find((m) => m.role === "assistant");
	const usageAvailable = hasReportedUsage(input.usage);
	return {
		ok: input.ok,
		exitCode: input.exitCode,
		...(input.error ? { error: input.error } : {}),
		...(input.errorCode ? { code: input.errorCode } : {}),
		model: input.model,
		source: input.source,
		durationMs: input.durationMs,
		usage: input.usage,
		usageAvailable,
		...(input.receipt ? { receipt: input.receipt } : {}),
		...(input.receiptRecord ? { receiptId: input.receiptRecord.id, receiptPath: input.receiptRecord.path } : {}),
		messageCount: messages.length,
		finalText: lastAssistant ? extractText(lastAssistant) : "",
		messages,
	};
}

function hasReportedUsage(usage: unknown): boolean {
	if (!usage || typeof usage !== "object") return false;
	const value = usage as Record<string, unknown>;
	return ["input", "output", "cacheRead", "cacheWrite", "totalTokens"].some(
		(key) => typeof value[key] === "number" && value[key] > 0,
	);
}

function subscribeStreamJson(bundle: AgentBundle, out: (s: string) => void): () => void {
	return bundle.subscribe((event: AgentEvent) => {
		// One AgentEvent per line. Inject a timestamp so consumers can
		// reason about latency without re-deriving from clock skew.
		out(`${JSON.stringify({ ...event, ts: Date.now() })}\n`);
	});
}

function subscribeText(bundle: AgentBundle, out: (s: string) => void, err: (s: string) => void): () => void {
	let lastStreamLen = 0;
	return bundle.subscribe((event: AgentEvent) => {
		switch (event.type) {
			case "tool_execution_start":
				err(`[${event.toolName}…]\n`);
				return;
			case "tool_execution_end":
				if (event.isError) err(`[${event.toolName} ERROR]\n`);
				return;
			case "message_update": {
				if (event.message.role !== "assistant") return;
				const text = extractText(event.message);
				if (text.length > lastStreamLen) {
					out(text.slice(lastStreamLen));
					lastStreamLen = text.length;
				}
				return;
			}
			case "message_end": {
				if (event.message.role !== "assistant") return;
				const text = extractText(event.message);
				if (text.length > lastStreamLen) out(text.slice(lastStreamLen));
				lastStreamLen = 0;
				out("\n");
				return;
			}
			case "agent_end":
				return;
		}
	});
}

function extractText(message: { content?: unknown }): string {
	if (typeof message.content === "string") return message.content;
	if (!Array.isArray(message.content)) return "";
	const parts: string[] = [];
	for (const block of message.content as Array<{ type: string; text?: string }>) {
		if (block.type === "text" && typeof block.text === "string") parts.push(block.text);
	}
	return parts.join("");
}

function latestAssistantText(messages: AgentMessage[]): string {
	const lastAssistant = [...messages]
		.reverse()
		.find((m) => m.role === "assistant" && extractText(m).trim().length > 0);
	return lastAssistant ? extractText(lastAssistant) : "";
}
