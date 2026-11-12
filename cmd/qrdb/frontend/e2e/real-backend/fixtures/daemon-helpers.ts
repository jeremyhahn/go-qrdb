// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Shared helpers used by both daemon.ts and sealed-daemon.ts.
 *
 * Extracted so findFreePort and waitForHealth can be used by the sealed
 * daemon fixture without duplicating code or creating circular imports.
 */

import * as net from "net";
import type { ChildProcess } from "child_process";

// ---------------------------------------------------------------------------
// Port utilities
// ---------------------------------------------------------------------------

/**
 * b64 encodes text for a JSON field the server declares as []byte (barrier
 * secrets, tenant passphrases and unseal keys): encoding/json reads such a
 * field as base64.
 */
export function b64(text: string): string {
  return Buffer.from(text, "utf8").toString("base64");
}

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
 */
export async function waitForHealthExported(
  adminUrl: string,
  timeoutMs = 20_000,
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
          res.resume();
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
        lastErr = null;
      },
      (err) => {
        lastErr = err;
      },
    );

    if (lastErr === null) {
      return;
    }

    await new Promise<void>((r) => setTimeout(r, 200));
  }

  throw new Error(
    `Daemon at ${adminUrl} did not become ready within ${timeoutMs}ms. Last error: ${lastErr}`,
  );
}


/** Bytes of daemon output kept for failure messages. */
const OUTPUT_TAIL_BYTES = 16 * 1024;
/** How long Supervised.stop waits after SIGTERM before it sends SIGKILL. */
export const STOP_GRACE_MS = 15_000;

/** A spawned daemon process watched from the moment it starts. */
export interface Supervised {
  proc: ChildProcess;
  /** Resolves once the process has exited (or failed to spawn). */
  exited: Promise<void>;
  /** null while running; otherwise how the process ended. */
  exitStatus: () => string | null;
  /** Tail of the process's stdout and stderr. */
  outputTail: () => string;
  /**
   * SIGTERM, then SIGKILL after STOP_GRACE_MS; resolves only once the
   * process has exited, so its ports and data dir are free.
   */
  stop: () => Promise<void>;
}

/**
 * Every daemon process this worker has started and not yet seen exit. If
 * the worker process ends without fixture teardown (a hard kill on
 * timeout), the exit hook SIGKILLs them so no daemon outlives its worker
 * and holds ports or CPU while the next worker boots its own daemon.
 */
const liveProcs = new Set<ChildProcess>();
process.once("exit", () => {
  for (const proc of liveProcs) {
    try {
      proc.kill("SIGKILL");
    } catch {
      // Already gone.
    }
  }
});

/**
 * supervise must be called right after spawn (stdio "pipe" for stdout and
 * stderr). It drains both pipes into a bounded tail (an undrained pipe
 * fills and blocks the daemon), records the exit status, and registers the
 * process for the worker-exit reaper.
 */
export function supervise(proc: ChildProcess): Supervised {
  liveProcs.add(proc);

  let tail = "";
  const append = (chunk: Buffer) => {
    tail += chunk.toString();
    if (tail.length > OUTPUT_TAIL_BYTES) tail = tail.slice(-OUTPUT_TAIL_BYTES);
  };
  proc.stdout?.on("data", append);
  proc.stderr?.on("data", append);

  let status: string | null = null;
  const exited = new Promise<void>((resolve) => {
    proc.once("exit", (code, signal) => {
      status = `code=${code} signal=${signal}`;
      liveProcs.delete(proc);
      resolve();
    });
    proc.once("error", (err) => {
      status ??= `spawn error: ${err.message}`;
      if (proc.pid === undefined) {
        // Never started: no exit event will follow.
        liveProcs.delete(proc);
        resolve();
      }
    });
  });

  const stop = async (): Promise<void> => {
    if (proc.exitCode === null && proc.signalCode === null && proc.pid !== undefined) {
      proc.kill("SIGTERM");
      let timer: NodeJS.Timeout | undefined;
      const timedOut = await Promise.race([
        exited.then(() => false),
        new Promise<boolean>((resolve) => {
          timer = setTimeout(() => resolve(true), STOP_GRACE_MS);
        }),
      ]);
      clearTimeout(timer);
      if (timedOut) {
        proc.kill("SIGKILL");
      }
    }
    await exited;
  };

  return { proc, exited, exitStatus: () => status, outputTail: () => tail, stop };
}

// ---------------------------------------------------------------------------
// Sealed-window polling
// ---------------------------------------------------------------------------

/**
 * Polls GET <adminUrl>/v1/barrier/status (ignoring TLS errors) until the
 * server responds with HTTP 200 and {sealed: true} in the JSON body, or
 * until the timeout expires.
 *
 * Use this helper instead of waitForHealthExported when spawning a daemon
 * with seal.enabled=true and an uninitialized barrier. In that mode the
 * daemon starts a sealed-window HTTP listener that serves /v1/barrier/*
 * while /health returns 503. The daemon is ready for barrier operations
 * (initialize, unseal) once this helper resolves.
 *
 * After initialization/unseal the daemon resumes full bootstrap and
 * /health transitions to 200. Use waitForHealthExported for that.
 */
export async function waitForSealedWindow(
  adminUrl: string,
  timeoutMs = 20_000,
  stopped: () => string | null = () => null,
): Promise<void> {
  const https = await import("https");
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown;

  while (Date.now() < deadline) {
    const why = stopped();
    if (why !== null) {
      throw new Error(`Daemon at ${adminUrl} exited before its sealed window opened (${why})`);
    }
    await new Promise<void>((resolve, reject) => {
      const req = https.default.get(
        `${adminUrl}/v1/barrier/status`,
        { rejectUnauthorized: false },
        (res) => {
          let data = "";
          res.on("data", (chunk: Buffer) => {
            data += chunk.toString();
          });
          res.on("end", () => {
            if (res.statusCode !== 200) {
              reject(new Error(`HTTP ${res.statusCode ?? "unknown"}`));
              return;
            }
            try {
              const body = JSON.parse(data) as Record<string, unknown>;
              if (body["sealed"] === true) {
                resolve();
              } else {
                reject(new Error(`barrier not sealed yet: ${data}`));
              }
            } catch (e) {
              reject(new Error(`failed to parse barrier status: ${data}`));
            }
          });
        },
      );
      req.on("error", reject);
      req.setTimeout(1000, () => {
        req.destroy();
        reject(new Error("request timeout"));
      });
    }).then(
      () => {
        lastErr = null;
      },
      (err: unknown) => {
        lastErr = err;
      },
    );

    if (lastErr === null) {
      return;
    }

    await new Promise<void>((r) => setTimeout(r, 200));
  }

  throw new Error(
    `Sealed-window at ${adminUrl} did not become ready within ${timeoutMs}ms. Last error: ${lastErr}`,
  );
}
