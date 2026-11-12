// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * dom-daemon.ts
 *
 * Daemon lifecycle helpers for the real-backend DOM test tier.
 *
 * Unlike the API-only real-backend tier (which drives REST directly), this
 * tier serves the actual Svelte UI via the Vite dev server and drives the real
 * DOM in Chromium against a live qrdbd. The Vite proxy target is fixed at
 * https://127.0.0.1:63012 (set via QRDB_PROXY_TARGET in playwright.dom.config),
 * so this helper always spawns the daemon on that port. The DOM tier therefore
 * runs single-worker (workers: 1) — only one daemon may own port 63012 at a
 * time.
 *
 * A daemon cannot serve the SPA itself while sealed (its sealed-window listener
 * exposes only the barrier endpoints), which is exactly why the SPA is served
 * by Vite and the API is proxied: the UI loads regardless of seal state and the
 * proxied /v1/barrier/* calls reach the sealed-window listener.
 *
 * Tests call startDaemon({ seal }) to boot a fresh daemon in the desired seal
 * mode, drive the UI, then the afterEach hook tears it down and wipes its data
 * directory so each test starts from a pristine, uninitialized state.
 */

import { spawn, type ChildProcess } from "child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// Fixed ports — must match QRDB_PROXY_TARGET in playwright.dom.config.ts.
export const DOM_ADMIN_PORT = 63012;
const DOM_RAFT_PORT = 63011;
const DOM_GRPC_PORT = 63013;

/**
 * The Vite dev-server origin the DOM specs navigate to. The DOM tier uses a
 * dedicated port (5273) distinct from the default dev server (5173) so it can
 * run alongside an interactive dev session without proxy-target conflicts.
 */
export const UI_BASE_URL = "https://localhost:5273";

/** Resolves the qrdbd binary (QRDBD_BIN overrides; falls back to bin/qrdbd). */
function qrdbdBin(): string {
  return (
    process.env["QRDBD_BIN"] ??
    join(__dirname, "..", "..", "..", "..", "..", "..", "bin", "qrdbd")
  );
}

/**
 * Supported global barrier seal modes for the DOM tier.
 *
 * "none" disables the seal subsystem entirely (no barrier). "none-committed"
 * enables the seal subsystem with the "none" strategy — a committed,
 * unencrypted barrier, which is the required starting state for the
 * enable-encryption (none -> keyed) migration wizard. Both boot unsealed.
 */
export type SealMode = "none" | "none-committed" | "passphrase" | "shamir";

export interface StartDaemonOptions {
  /** Global barrier mode. "none" boots unsealed; the others boot sealed. */
  seal: SealMode;
  /** Shamir threshold (M). Only used when seal === "shamir". Default 2. */
  shamirThreshold?: number;
  /** Shamir total shares (N). Only used when seal === "shamir". Default 3. */
  shamirTotal?: number;
  /**
   * TLS client-auth mode passed as --tls-client-auth=<value>. Defaults to
   * "none". The quorum-seal spec sets "request" so officers can present
   * their mTLS certificates during the share-claim handshake: "request"
   * asks for a client cert without CA-verifying it at the TLS layer (the
   * claim handler's SHA-256 thumbprint binding is the actual verification),
   * while the Vite proxy — which carries no client cert — keeps working for
   * all regular UI traffic.
   */
  tlsClientAuth?: "none" | "request";
  /**
   * State machine type passed as --sm=<value>. Defaults to the daemon's
   * default (pebble). Lock listing and status need an MVCC-backed shard,
   * so the lock spec boots with "mvcc".
   */
  sm?: "mvcc";
  /**
   * Volume pool names to map to local "dir" mounts (storage.volume_pools).
   * Each pool gets a directory at <dataDir>-pools/<name>: config validation
   * refuses a pool path inside the node's data directory. The pools are
   * also granted to new tenants (tenant.default_entitlements: volume
   * isolation and allowed_volume_pools). The volume service is built only
   * over a barrier, so pair this with a seal other than "none".
   */
  volumePools?: string[];
}

export interface DomDaemon {
  adminUrl: string;
  dataDir: string;
  proc: ChildProcess;
}

function writeConfig(path: string, opts: StartDaemonOptions): void {
  const lines = ['codec:', '  storage_codec: "json"', '  wire_codec: "json"', "seal:"];
  if (opts.seal === "none") {
    // auth.enabled requires seal.enabled, so a seal-off daemon runs auth-off.
    lines.push("  enabled: false");
    lines.push("auth:", "  enabled: false");
  } else if (opts.seal === "none-committed") {
    lines.push("  enabled: true", '  strategy: "none"');
  } else {
    lines.push("  enabled: true", `  strategy: "${opts.seal}"`);
    if (opts.seal === "shamir") {
      lines.push(`  shamir_threshold: ${opts.shamirThreshold ?? 2}`);
      lines.push(`  shamir_total: ${opts.shamirTotal ?? 3}`);
    }
  }
  if (opts.volumePools !== undefined && opts.volumePools.length > 0) {
    const poolRoot = `${dirname(path)}-pools`;
    lines.push("storage:", "  volume_pools:");
    for (const name of opts.volumePools) {
      const poolDir = join(poolRoot, name);
      mkdirSync(poolDir, { recursive: true, mode: 0o700 });
      lines.push(`    - name: "${name}"`, `      path: "${poolDir}"`, '      provider: "dir"');
    }
    // Entitlements are fail-closed and a create request carries none, so a
    // new tenant may use the pools only through the operator defaults.
    lines.push(
      "tenant:",
      "  default_entitlements:",
      '    allowed_isolation_modes: ["prefix", "barrier", "volume"]',
      "    allow_volume_storage: true",
      `    allowed_volume_pools: [${opts.volumePools.map((n) => `"${n}"`).join(", ")}]`,
    );
  }
  writeFileSync(path, lines.join("\n") + "\n", { mode: 0o600 });
}

/**
 * Polls the daemon until it is ready to receive UI traffic:
 *   - "none": /health returns 200 (fully booted, unsealed).
 *   - sealed modes: /v1/barrier/status returns 200 (sealed-window listener up).
 */
async function waitForReady(adminUrl: string, seal: SealMode, timeoutMs = 20_000): Promise<void> {
  const https = await import("https");
  // seal:"none" (no barrier) boots straight to healthy. Every enabled-seal
  // mode — including "none-committed", whose barrier still needs an
  // initialize call — boots into the sealed-window listener first.
  const path = seal === "none" ? "/health" : "/v1/barrier/status";
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown = "no attempt";
  while (Date.now() < deadline) {
    const ok = await new Promise<boolean>((resolve) => {
      const req = https.default.get(`${adminUrl}${path}`, { rejectUnauthorized: false }, (res) => {
        res.resume();
        resolve(res.statusCode === 200);
      });
      req.on("error", (e) => {
        lastErr = e;
        resolve(false);
      });
      req.setTimeout(1000, () => {
        req.destroy();
        resolve(false);
      });
    });
    if (ok) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`DOM daemon at ${adminUrl}${path} not ready within ${timeoutMs}ms (last: ${lastErr})`);
}

/**
 * Spawns a fresh qrdbd on the fixed DOM ports in the requested seal mode and
 * blocks until it is ready for UI traffic.
 */
export async function startDaemon(opts: StartDaemonOptions): Promise<DomDaemon> {
  const dataDir = mkdtempSync(join(tmpdir(), "qrdb-dom-"));
  const configPath = join(dataDir, "config.yaml");
  writeConfig(configPath, opts);

  const args = [
    "start",
    "--config", configPath,
    "--data-dir", dataDir,
    "--listen", `127.0.0.1:${DOM_RAFT_PORT}`,
    "--raft-address", `127.0.0.1:${DOM_RAFT_PORT}`,
    "--admin-port", String(DOM_ADMIN_PORT),
    "--grpc-port", String(DOM_GRPC_PORT),
    "--auth-enabled=false",
    `--tls-client-auth=${opts.tlsClientAuth ?? "none"}`,
    "--discovery", "static",
    "--peers", `1=127.0.0.1:${DOM_RAFT_PORT}`,
    "--replica-id", "1",
    "--log-level", "warn",
    "--enable-grpc=false",
    "--enable-unix=false",
    "--enable-quic=false",
    "--enable-mcp=false",
    ...(opts.sm !== undefined ? ["--sm", opts.sm] : []),
  ];

  const proc = spawn(qrdbdBin(), args, {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, NO_COLOR: "1" },
  });
  const stderr: Buffer[] = [];
  proc.stderr?.on("data", (c: Buffer) => stderr.push(c));

  const adminUrl = `https://127.0.0.1:${DOM_ADMIN_PORT}`;
  await new Promise((r) => setTimeout(r, 300));
  try {
    await waitForReady(adminUrl, opts.seal);
  } catch (err) {
    proc.kill("SIGTERM");
    throw new Error(`${err}\nstderr:\n${Buffer.concat(stderr).toString()}`);
  }
  return { adminUrl, dataDir, proc };
}

/**
 * Polls /health until it returns 200. After a barrier is initialized/unsealed
 * the daemon tears down its sealed-window listener and resumes full bootstrap;
 * during that window /health briefly returns 503 then 404 before 200. Tests
 * call this before navigating into the post-init app shell.
 */
export async function waitForHealthy(daemon: DomDaemon, timeoutMs = 30_000): Promise<void> {
  const https = await import("https");
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ok = await new Promise<boolean>((resolve) => {
      const req = https.default.get(`${daemon.adminUrl}/health`, { rejectUnauthorized: false }, (res) => {
        res.resume();
        resolve(res.statusCode === 200);
      });
      req.on("error", () => resolve(false));
      req.setTimeout(1000, () => {
        req.destroy();
        resolve(false);
      });
    });
    if (ok) return;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`DOM daemon ${daemon.adminUrl}/health not 200 within ${timeoutMs}ms`);
}

/** Stops the daemon and removes its data directory (best effort). */
export async function stopDaemon(daemon: DomDaemon | null): Promise<void> {
  if (daemon === null) return;
  daemon.proc.kill("SIGTERM");
  await new Promise((r) => setTimeout(r, 400));
  try {
    rmSync(daemon.dataDir, { recursive: true, force: true });
  } catch {
    // best effort
  }
}
