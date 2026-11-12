/**
 * The few Node built-ins pkcs12.test.ts uses to drive OpenSSL. The SDK has
 * no @types/node dependency (it targets browsers too), so these are
 * declared here, test-only, the way raw-modules.d.ts declares `?raw`.
 */

declare module 'node:child_process' {
    export function execFileSync(
        file: string,
        args: readonly string[],
        options?: { encoding?: 'utf8'; cwd?: string; stdio?: unknown },
    ): string;
}

declare module 'node:fs' {
    export function mkdtempSync(prefix: string): string;
    export function readFileSync(path: string, encoding: 'utf8'): string;
    export function writeFileSync(path: string, data: string | Uint8Array): void;
    export function rmSync(path: string, options?: { recursive?: boolean; force?: boolean }): void;
}

declare module 'node:os' {
    export function tmpdir(): string;
}

declare module 'node:path' {
    export function join(...parts: string[]): string;
}
