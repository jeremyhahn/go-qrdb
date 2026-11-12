// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * auth-daemon.ts
 *
 * Daemon fixture variant with auth-enabled=true.
 *
 * This fixture spawns an ephemeral qrdbd process with authentication and
 * TLS client certificate verification enabled. It is used by specs that need
 * to verify bootstrap artifacts (admin-cert.pem, admin-key.pem) and
 * auth-gated endpoints.
 *
 * The fixture exposes:
 *   - adminUrl: the HTTPS base URL
 *   - dataDir: the temporary data directory
 *   - adminCertPath / adminKeyPath: paths to the generated admin mTLS cert
 *   - caCertPath: path to the auto-generated CA cert (for trust anchoring)
 */

import { test as base } from "@playwright/test";
import { spawn } from "child_process";
import { mkdtempSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import { findFreePort, supervise, waitForHealthExported } from "./daemon-helpers.ts";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

function qrdbdBin(): string {
  return (
    process.env["QRDBD_BIN"] ??
    join(__dirname, "..", "..", "..", "..", "..", "..", "bin", "qrdbd")
  );
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export interface AuthDaemonInfo {
  adminUrl: string;
  adminPort: number;
  raftPort: number;
  grpcPort: number;
  dataDir: string;
  /** Path to admin-cert.pem (written only when auth-enabled=true). */
  adminCertPath: string;
  /** Path to admin-key.pem (written only when auth-enabled=true). */
  adminKeyPath: string;
  /** Path to ca.pem used to verify the daemon's TLS cert. */
  caCertPath: string;
  /** Path to node-cert.pem written on first boot. */
  nodeCertPath: string;
  /** Path to node-key.pem written on first boot. */
  nodeKeyPath: string;
}

export type AuthDaemonFixtures = {
  authDaemon: AuthDaemonInfo;
};

// ---------------------------------------------------------------------------
// Fixture implementation
// ---------------------------------------------------------------------------

export const test = base.extend<AuthDaemonFixtures>({
  authDaemon: async ({}, use) => {
    const [adminPort, raftPort, grpcPort] = await Promise.all([
      findFreePort(),
      findFreePort(),
      findFreePort(),
    ]);

    const dataDir = mkdtempSync(join(tmpdir(), "qrdb-auth-e2e-"));

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
      // Auth enabled — this triggers admin cert generation. No explicit
      // --tls-client-auth: the auth-derived default is
      // VerifyClientCertIfGiven, so public endpoints work without a
      // client cert while the cert is validated when provided.
      "--auth-enabled=true",
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
    ];

    const proc = spawn(qrdbdBin(), args, {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, NO_COLOR: "1" },
    });

    // Drains stdout/stderr into a tail, records the exit, and reaps the
    // process if the worker exits without this teardown.
    const sup = supervise(proc);

    const adminUrl = `https://127.0.0.1:${adminPort}`;
    const info: AuthDaemonInfo = {
      adminUrl,
      adminPort,
      raftPort,
      grpcPort,
      dataDir,
      adminCertPath: join(dataDir, "admin-cert.pem"),
      adminKeyPath: join(dataDir, "admin-key.pem"),
      caCertPath: join(dataDir, "ca.pem"),
      nodeCertPath: join(dataDir, "node-cert.pem"),
      nodeKeyPath: join(dataDir, "node-key.pem"),
    };

    try {
      await waitForHealthExported(adminUrl, undefined, sup.exitStatus);
    } catch (err) {
      const exit = sup.exitStatus() ?? "still running (stopped by fixture)";
      await sup.stop();
      rmSync(dataDir, { recursive: true, force: true });
      throw new Error(
        `[auth-daemon] health check failed.\nArgs: ${args.join(" ")}\nExit: ${exit}\nOutput tail:\n${sup.outputTail()}\nOriginal: ${err}`,
      );
    }

    await use(info);

    // Wait for the exit (SIGKILL after the grace period) so the next
    // test's daemon never races this one for CPU, ports or files.
    await sup.stop();
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  },
});

export { expect } from "@playwright/test";
