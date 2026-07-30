import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

declare const CODEBASE_BUILD_VERSION: string | undefined;

/**
 * Native builds replace CODEBASE_BUILD_VERSION at compile time. The npm
 * package keeps package.json beside dist/, so regular Node installs resolve
 * the same source of truth from disk without JSON-module import semantics.
 */
export const VERSION: string = (() => {
	if (typeof CODEBASE_BUILD_VERSION !== "undefined" && CODEBASE_BUILD_VERSION) {
		return CODEBASE_BUILD_VERSION;
	}

	try {
		const here = dirname(fileURLToPath(import.meta.url));
		const pkg = JSON.parse(readFileSync(join(here, "..", "package.json"), "utf8")) as { version?: string };
		return pkg.version ?? "?.?.?";
	} catch {
		return "?.?.?";
	}
})();
