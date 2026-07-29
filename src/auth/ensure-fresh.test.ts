import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CredentialsStore } from "./credentials.js";
import { ensureFreshCredentials } from "./ensure-fresh.js";

describe("ensureFreshCredentials", () => {
	let home: string;
	let previousHome: string | undefined;

	beforeEach(() => {
		previousHome = process.env.HOME;
		home = mkdtempSync(join(tmpdir(), "ensure-fresh-"));
		process.env.HOME = home;
	});

	afterEach(() => {
		if (previousHome === undefined) delete process.env.HOME;
		else process.env.HOME = previousHome;
		rmSync(home, { recursive: true, force: true });
		vi.restoreAllMocks();
	});

	it("refreshes within the five-minute preflight window", async () => {
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

		await ensureFreshCredentials();

		expect(fetchSpy).toHaveBeenCalledTimes(1);
		expect(store.load()?.accessToken).toBe("access-new");
	});
});
