// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Shared helpers used by both daemon.ts and sealed-daemon.ts.
 *
 * Extracted so findFreePort and waitForHealth can be used by the sealed
 * daemon fixture without duplicating code or creating circular imports.
 */

import * as net from "net";

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
): Promise<void> {
  const https = await import("https");
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown;

  while (Date.now() < deadline) {
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
): Promise<void> {
  const https = await import("https");
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown;

  while (Date.now() < deadline) {
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
              reject(new Error(`failed to parse barrier status: ${data}`, { cause: e }));
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
