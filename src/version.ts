import packageMetadata from "../package.json";

/**
 * Keep package.json as the single source of truth, but import it so native
 * Bun builds embed the version instead of trying to read a neighboring file
 * that does not exist beside a standalone binary.
 */
export const VERSION = packageMetadata.version;
