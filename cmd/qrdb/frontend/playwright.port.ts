// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Per-run port selection for the Vite dev server that playwright.config.ts
 * starts, and for the backend its API proxy forwards to.
 *
 * CI job containers use host networking, so two concurrent jobs on one runner
 * host share a loopback interface. A fixed dev-server port makes the second job
 * fail with "http://localhost:5173 is already used". A fixed proxy target is
 * worse: the mocked tier has no backend, so a request a spec leaves unmocked
 * would reach whatever another job's qrdbd happens to serve on that port. Each
 * run therefore asks the kernel for free ports instead. The kernel hands out
 * listen(0) ports from the ephemeral range, which the qrdbd test harnesses
 * keep their own ports below.
 */

import { createServer, type Server } from "node:net";

/** Names the env var that pins the dev-server port for a run. */
export const PORT_ENV = "PLAYWRIGHT_PORT";

/**
 * Names the env var that pins the port the dev server's API proxy forwards to
 * for a run. Nothing listens there in the mocked tier, so a request a spec
 * leaves unmocked is refused instead of reaching a real daemon.
 */
export const PROXY_PORT_ENV = "PLAYWRIGHT_PROXY_PORT";

/** The loopback address the dev server binds and the tests browse. */
export const HOST = "127.0.0.1";

/**
 * holdFreePort binds an ephemeral loopback port and keeps it bound, so ports
 * picked while earlier ones are still held are guaranteed distinct. The caller
 * releases it and must pass the number to a server started with --strictPort,
 * so that losing the (small) race to another process fails loudly instead of
 * silently serving from a different port.
 */
function holdFreePort(): Promise<Server> {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.unref();
    srv.on("error", reject);
    srv.listen(0, HOST, () => resolve(srv));
  });
}

/** boundPort returns the port srv is listening on. */
function boundPort(srv: Server): number {
  const addr = srv.address();
  if (addr === null || typeof addr === "string") {
    throw new Error("playwright.port: no port assigned");
  }
  return addr.port;
}

/** pinnedPort returns the port env pins, or undefined when it pins none. */
function pinnedPort(env: string): number | undefined {
  const pinned = process.env[env];
  if (pinned === undefined || pinned === "") {
    return undefined;
  }
  const port = Number(pinned);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`${env}=${pinned} is not a valid port`);
  }
  return port;
}

/**
 * resolvePorts returns one distinct port per env var named, in order. A value
 * already set in an env var wins. Every other port is chosen free and written
 * back to its env var: Playwright loads the config again in every worker
 * process, and the workers inherit the environment, so they all see the ports
 * the runner picked rather than each choosing their own.
 */
export async function resolvePorts(...envs: string[]): Promise<number[]> {
  const held: Server[] = [];
  try {
    const ports: number[] = [];
    for (const env of envs) {
      let port = pinnedPort(env);
      if (port === undefined) {
        const srv = await holdFreePort();
        held.push(srv);
        port = boundPort(srv);
        process.env[env] = String(port);
      }
      ports.push(port);
    }
    if (new Set(ports).size !== ports.length) {
      throw new Error(`${envs.join(", ")} must name distinct ports, got ${ports.join(", ")}`);
    }
    return ports;
  } finally {
    await Promise.all(held.map((srv) => new Promise((done) => srv.close(done))));
  }
}
