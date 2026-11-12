// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Real-backend daemon fixture.
 *
 * Spawns a real qrdbd binary in a temporary directory, waits for the
 * health endpoint to become ready, then exposes the admin URL to specs.
 * The daemon is torn down and the temporary directory is removed after
 * the test suite completes.
 *
 * Critically this fixture NEVER touches ports 63002 or 63402 which may
 * be used by live sessions or other test runs.
 */

import { test as base } from "@playwright/test";
import { spawn, type ChildProcess } from "child_process";
import { mkdtempSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import * as net from "net";
import { supervise, type Supervised } from "./daemon-helpers.ts";

// ES module equivalent of __dirname
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// ---------------------------------------------------------------------------
// Port utilities
// ---------------------------------------------------------------------------

/**
 * Finds a free TCP port on 127.0.0.1 by binding to port 0 and reading
 * the OS-assigned port back. Returns the port number.
 */
export function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address() as net.AddressInfo;
      srv.close((err) => {
        if (err) {
          reject(err);
        } else {
          resolve(addr.port);
        }
      });
    });
    srv.on("error", reject);
  });
}

// ---------------------------------------------------------------------------
// Health polling
// ---------------------------------------------------------------------------

/**
 * Polls GET <adminUrl>/health (ignoring TLS errors) until the server returns
 * status 200 or the timeout expires. Throws if the daemon does not become
 * ready within the timeout.
 *
 * Uses the `https` Node module directly with `rejectUnauthorized: false` so
 * self-signed certificates are accepted without relying on fetch dispatcher
 * options that vary across Node versions.
 */
async function waitForHealth(
  adminUrl: string,
  timeoutMs = 15_000,
  stopped: () => string | null = () => null,
): Promise<void> {
  const https = await import("https");
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown;

  while (Date.now() < deadline) {
    // A daemon that has already exited will never answer: fail at once
    // instead of polling a dead port for the rest of the timeout.
    const why = stopped();
    if (why !== null) {
      throw new Error(`Daemon at ${adminUrl} exited before becoming ready (${why})`);
    }
    await new Promise<void>((resolve, reject) => {
      const req = https.default.get(
        `${adminUrl}/health`,
        { rejectUnauthorized: false },
        (res) => {
          res.resume(); // drain response
          if (res.statusCode === 200) {
            resolve();
          } else {
            reject(new Error(`HTTP ${res.statusCode ?? "unknown"}`));
          }
        },
      );
      req.on("error", reject);
      req.setTimeout(1000, () => {
        req.destroy();
        reject(new Error("request timeout"));
      });
    }).then(
      () => {
        // health returned 200 — break out of polling
        lastErr = null;
      },
      (err) => {
        lastErr = err;
      },
    );

    if (lastErr === null) {
      return;
    }

    await new Promise((r) => setTimeout(r, 200));
  }

  throw new Error(
    `Daemon at ${adminUrl} did not become ready within ${timeoutMs}ms. Last error: ${lastErr}`,
  );
}

// ---------------------------------------------------------------------------
// Daemon fixture state
// ---------------------------------------------------------------------------

/** Information about the running test daemon. */
export interface DaemonInfo {
  /** Full admin base URL, e.g. https://127.0.0.1:54321 */
  adminUrl: string;
  /** Ephemeral admin port */
  adminPort: number;
  /** Ephemeral Raft / listen port */
  raftPort: number;
  /** Ephemeral gRPC port */
  grpcPort: number;
  /** Temporary data directory (cleaned up in afterAll) */
  dataDir: string;
}

// ---------------------------------------------------------------------------
// Daemon fixture definition
// ---------------------------------------------------------------------------

export type DaemonFixtures = {
  /** Live daemon info exposed to each test in the real-backend suite. */
  daemon: DaemonInfo;
  /**
   * Live daemon info for a SECOND, raft.sm_type: mvcc daemon, exposed only
   * to specs that request it (e.g. kv-restore-mvcc-real.spec.ts). Spawned
   * lazily, independently of `daemon`, and cached per worker exactly like
   * `daemon` -- see ensureMvccDaemon.
   */
  mvccDaemon: DaemonInfo;
};

/**
 * qrdbdBin is resolved relative to the repository root. CI sets the env var
 * QRDBD_BIN to override. Falls back to the pre-built binary at bin/qrdbd.
 */
function qrdbdBin(): string {
  return (
    process.env["QRDBD_BIN"] ??
    join(__dirname, "..", "..", "..", "..", "..", "..", "bin", "qrdbd")
  );
}

/**
 * spawnDaemon starts a real qrdbd binary with the fixture's standard base
 * flags (ephemeral ports, no auth, plaintext data dir) plus any extraArgs,
 * and blocks until its health endpoint responds. Shared by ensureDaemon
 * (the default, no-"--sm" daemon every OTHER real-backend spec uses) and
 * ensureMvccDaemon (adds "--sm mvcc"), so the two only ever differ in the
 * one flag that matters for this feature.
 */
async function spawnDaemon(extraArgs: string[] = []): Promise<RunningDaemon> {
  let lastErr: unknown;
  for (let attempt = 1; attempt <= SPAWN_ATTEMPTS; attempt++) {
    try {
      return await spawnDaemonOnce(extraArgs);
    } catch (err) {
      lastErr = err;
      // Only a daemon that exited early (e.g. a port taken between
      // findFreePort and bind) is worth another try with fresh ports and
      // a fresh data dir; a daemon that stayed up but never answered is
      // a real failure.
      if (!(err instanceof DaemonStartError) || !err.exitedEarly) break;
    }
  }
  throw lastErr;
}

/** Spawn attempts, each with fresh ports and a fresh data dir. */
const SPAWN_ATTEMPTS = 3;

/** A daemon process this worker started, with its supervision handle. */
interface RunningDaemon extends Supervised {
  info: DaemonInfo;
}

/** Error from spawnDaemonOnce; exitedEarly marks a daemon that died before ready. */
class DaemonStartError extends Error {
  constructor(
    message: string,
    readonly exitedEarly: boolean,
  ) {
    super(message);
  }
}

/** stopDaemon stops a daemon (waiting for its exit) and removes its data dir. */
async function stopDaemon(d: RunningDaemon): Promise<void> {
  await d.stop();
  rmSync(d.info.dataDir, { recursive: true, force: true });
}

/**
 * spawnDaemonOnce starts one daemon on fresh ports with a fresh data dir
 * and waits for its health endpoint. On failure the daemon is stopped, its
 * data dir removed, and the error carries the args, the exit status and the
 * daemon's output tail.
 */
async function spawnDaemonOnce(extraArgs: string[]): Promise<RunningDaemon> {
  const [adminPort, raftPort, grpcPort] = await Promise.all([
    findFreePort(),
    findFreePort(),
    findFreePort(),
  ]);

  const dataDir = mkdtempSync(join(tmpdir(), "qrdb-e2e-"));

  const args = [
    "start",
    "--data-dir",
    dataDir,
    "--listen",
    `127.0.0.1:${raftPort}`,
    "--raft-address",
    `127.0.0.1:${raftPort}`,
    "--admin-port",
    String(adminPort),
    "--grpc-port",
    String(grpcPort),
    "--auth-enabled=false",
    "--tls-client-auth=none",
    "--discovery",
    "static",
    "--peers",
    `1=127.0.0.1:${raftPort}`,
    "--replica-id",
    "1",
    "--log-level",
    "warn",
    "--enable-grpc=false",
    "--enable-unix=false",
    "--enable-quic=false",
    "--enable-mcp=false",
    ...extraArgs,
  ];

  const proc = spawn(qrdbdBin(), args, {
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      ...process.env,
      // Suppress color codes in log output.
      NO_COLOR: "1",
    },
  });
  const sup = supervise(proc);

  const adminUrl = `https://127.0.0.1:${adminPort}`;
  const info: DaemonInfo = { adminUrl, adminPort, raftPort, grpcPort, dataDir };
  const running: RunningDaemon = { ...sup, info };

  try {
    await waitForHealth(adminUrl, 15_000, sup.exitStatus);
  } catch (err) {
    const exitStatus = sup.exitStatus();
    const exitedEarly = exitStatus !== null;
    await stopDaemon(running);
    throw new DaemonStartError(
      `[daemon fixture] daemon health check failed.\nArgs: ${args.join(" ")}\n` +
        `Exit: ${exitStatus ?? "still running (stopped by fixture)"}\n` +
        `Output tail:\n${sup.outputTail()}\nOriginal: ${err}`,
      exitedEarly,
    );
  }

  return running;
}

// Shared daemon state across all tests in the worker (spawned once per worker).
let sharedDaemon: RunningDaemon | null = null;

/**
 * watchShared clears the shared slot (and removes the data dir) when a
 * shared daemon dies on its own, so the next test starts a new one.
 */
function watchShared(shared: RunningDaemon, label: string, clear: (d: RunningDaemon) => void): void {
  shared.proc.once("exit", (code, signal) => {
    if (code !== 0 && signal !== "SIGTERM" && signal !== "SIGKILL") {
      console.warn(
        `[daemon fixture] ${label} exited unexpectedly: code=${code} signal=${signal}\n${shared.outputTail()}`,
      );
    }
    clear(shared);
  });
}

/**
 * ensureDaemon starts the default (no "--sm" flag, i.e. raft.sm_type:
 * pebble) daemon if it is not already running. Uses a module-level
 * singleton so we only spawn once per Playwright worker.
 */
async function ensureDaemon(): Promise<DaemonInfo> {
  if (sharedDaemon !== null) {
    return sharedDaemon.info;
  }
  const shared = await spawnDaemon();
  watchShared(shared, "daemon", (d) => {
    if (sharedDaemon === d) sharedDaemon = null;
  });
  sharedDaemon = shared;
  return shared.info;
}

// Shared raft.sm_type: mvcc daemon state, independent of sharedDaemon.
let sharedMvccDaemon: RunningDaemon | null = null;

/**
 * ensureMvccDaemon starts a raft.sm_type: mvcc daemon (identical to
 * ensureDaemon's, plus "--sm mvcc") if one is not already running for this
 * worker. Used only by specs that need a real, MVCC-backed daemon -- e.g.
 * kv-restore-mvcc-real.spec.ts, which drives an actual end-to-end restore.
 */
async function ensureMvccDaemon(): Promise<DaemonInfo> {
  if (sharedMvccDaemon !== null) {
    return sharedMvccDaemon.info;
  }
  const shared = await spawnDaemon(["--sm", "mvcc"]);
  watchShared(shared, "mvcc daemon", (d) => {
    if (sharedMvccDaemon === d) sharedMvccDaemon = null;
  });
  sharedMvccDaemon = shared;
  return shared.info;
}

/**
 * Stops the shared daemon, waiting for it to exit, and removes its
 * temporary data directory. Called by the worker teardown below.
 */
export async function shutdownDaemon(): Promise<void> {
  if (sharedDaemon === null) return;
  const d = sharedDaemon;
  sharedDaemon = null;
  await stopDaemon(d);
}

/**
 * Stops the shared mvcc daemon, waiting for it to exit, and removes its
 * temporary data directory. Called by the worker teardown below.
 */
export async function shutdownMvccDaemon(): Promise<void> {
  if (sharedMvccDaemon === null) return;
  const d = sharedMvccDaemon;
  sharedMvccDaemon = null;
  await stopDaemon(d);
}

// ---------------------------------------------------------------------------
// Extended test with daemon fixture
// ---------------------------------------------------------------------------

type DaemonWorkerFixtures = {
  /**
   * Worker-scoped teardown: Playwright replaces the worker after a failed
   * test, and this stops the old worker's daemons (waiting for exit) before
   * the replacement worker starts its own.
   */
  daemonReaper: void;
};

export const test = base.extend<DaemonFixtures, DaemonWorkerFixtures>({
  daemonReaper: [
    async ({}, use) => {
      await use();
      await Promise.all([shutdownDaemon(), shutdownMvccDaemon()]);
    },
    { scope: "worker", auto: true },
  ],
  daemon: async ({}, use) => {
    const info = await ensureDaemon();
    await use(info);
  },
  mvccDaemon: async ({}, use) => {
    const info = await ensureMvccDaemon();
    await use(info);
  },
});

export { expect } from "@playwright/test";
