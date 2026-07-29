import type { AgentMessage } from "@earendil-works/pi-agent-core";

const PROVIDER_AUTH_PATTERNS = [
	/\bauthentication_error\b/i,
	/\binvalid[_ -]?(?:x-)?api[_ -]?key\b/i,
	/\bunauthorized\b/i,
	/\b401\b/,
];

export function providerAuthRecoveryMessage(raw: string): string | undefined {
	const trimmed = raw.trim();
	if (!trimmed) return undefined;
	if (!PROVIDER_AUTH_PATTERNS.some((re) => re.test(trimmed))) return undefined;
	if (!/(api[_ -]?key|x-api-key|authentication|unauthorized|401)/i.test(trimmed)) return undefined;
	return [
		"That API key was rejected by the provider.",
		"Run `codebase --new` to paste a new BYOK key, `codebase auth login` to use Codebase credits, or `/model` to switch providers.",
	].join(" ");
}

export function providerCreditsRecoveryMessage(raw: string): string | undefined {
	const trimmed = raw.trim();
	if (!trimmed || !/(\b402\b|insufficient[_ -]?credits?)/i.test(trimmed)) return undefined;
	return [
		"Codebase credits are exhausted.",
		"Run `codebase usage` to check the balance, add credits in Codebase Settings, wait for the plan reset, or switch to a BYOK model.",
	].join(" ");
}

export function userFacingErrorMessage(raw: string): string {
	return providerAuthRecoveryMessage(raw) ?? providerCreditsRecoveryMessage(raw) ?? raw.trim();
}

export function latestAssistantError(messages: AgentMessage[]): string | undefined {
	const lastAssistant = [...messages].reverse().find((message) => message.role === "assistant");
	if (!lastAssistant) return undefined;
	const candidate = lastAssistant as { stopReason?: unknown; errorMessage?: unknown };
	if (typeof candidate.errorMessage === "string" && candidate.errorMessage.trim()) return candidate.errorMessage;
	if (candidate.stopReason === "error") return "Agent turn ended with an error.";
	return undefined;
}
