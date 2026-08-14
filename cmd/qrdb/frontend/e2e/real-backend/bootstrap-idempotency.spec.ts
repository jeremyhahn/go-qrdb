// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * bootstrap-idempotency.spec.ts
 *
 * Validates restart safety: running the daemon a second time against the
 * same data directory must not recreate the admin user, regenerate the
 * bootstrap PKI artifacts (admin-cert.pem / admin-key.pem / ca.pem), or
 * lose committed KV state.
 *
 * The suite spawns its own auth-enabled daemon, captures bootstrap state,
 * stops it with SIGTERM, restarts it against the same data directory and
 * ports, and asserts idempotency. The historical restart-hang bug (daemon
 * stalled after "admin client certificate already exists" on second boot)
 * is fixed; a hung restart now fails the beforeAll health wait.
 *
 * Authenticated calls present the bootstrap admin mTLS cert; the user
 * lookup uses GET /users/by-username/admin (UsersRead).
 */

import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "child_process";
import { createHash } from "crypto";
import { mkdtempSync, readFileSync, rmSync, statSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import { findFreePort } from "./fixtures/daemon-helpers.ts";

const __dirname = dirname(fileURLToPath(import.meta.url));

function qrdbdBin(): string {
  return (
    process.env["QRDBD_BIN"] ??
    join(__dirname, "..", "..", "..", "..", "..", "bin", "qrdbd")
  );
}

interface UserResponse {
  id: string;
  username: string;
  tenant_id: string;
  role_ids: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

interface ArtifactState {
  sha256: string;
  mtimeMs: number;
}

const ARTIFACTS = ["admin-cert.pem", "admin-key.pem", "ca.pem"] as const;

let dataDir: string;
let adminUrl: string;
let ports: { admin: number; raft: number; grpc: number };
let proc: ChildProcess | null = null;

let firstBootArtifacts: Record<string, ArtifactState>;
let firstBootAdmin: UserResponse;
const kvKey = `bootstrap-idem-${Date.now()}`;
const kvValue = "survives-restart";

function captureArtifacts(): Record<string, ArtifactState> {
  const out: Record<string, ArtifactState> = {};
  for (const name of ARTIFACTS) {
    const p = join(dataDir, name);
    out[name] = {
      sha256: createHash("sha256").update(readFileSync(p)).digest("hex"),
      mtimeMs: statSync(p).mtimeMs,
    };
  }
  return out;
}

/** HTTPS request presenting the bootstrap admin mTLS client cert. */
async function adminHttps(
  method: string,
  path: string,
  body?: string,
  contentType?: string,
): Promise<{ status: number; text: string }> {
  const mod = await import("https");
  return await new Promise((resolve, reject) => {
    const req = mod.default.request(
      `${adminUrl}${path}`,
      {
        method,
        rejectUnauthorized: false,
        cert: readFileSync(join(dataDir, "admin-cert.pem")),
        key: readFileSync(join(dataDir, "admin-key.pem")),
        headers: contentType ? { "Content-Type": contentType } : {},
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () =>
          resolve({
            status: res.statusCode ?? 0,
            text: Buffer.concat(chunks).toString(),
          }),
        );
      },
    );
    req.on("error", reject);
    if (body !== undefined) req.write(body);
    req.end();
  });
}

function startDaemon(): ChildProcess {
  return spawn(
    qrdbdBin(),
    [
      "start", "--data-dir", dataDir,
      "--listen", `127.0.0.1:${ports.raft}`,
      "--raft-address", `127.0.0.1:${ports.raft}`,
      "--admin-port", String(ports.admin),
      "--grpc-port", String(ports.grpc),
      // No explicit --tls-client-auth: with auth enabled the daemon
      // defaults to VerifyClientCertIfGiven, which both serves /health
      // without a cert and authenticates the admin mTLS cert.
      "--auth-enabled=true",
      "--discovery", "static", "--peers", `1=127.0.0.1:${ports.raft}`,
      "--replica-id", "1", "--log-level", "warn",
      "--enable-grpc=false", "--enable-unix=false",
      "--enable-quic=false", "--enable-mcp=false",
    ],
    { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, NO_COLOR: "1" } },
  );
}

async function waitHealth(timeoutMs: number): Promise<void> {
  const mod = await import("https");
  const deadline = Date.now() + timeoutMs;
  // Poll /health until it serves 200 or the deadline passes.
  for (;;) {
    const status = await new Promise<number>((resolve) => {
      const req = mod.default.request(
        `${adminUrl}/health`,
        { method: "GET", rejectUnauthorized: false, timeout: 2_000 },
        (res) => {
          res.resume();
          resolve(res.statusCode ?? 0);
        },
      );
      req.on("error", () => resolve(0));
      req.on("timeout", () => {
        req.destroy();
        resolve(0);
      });
      req.end();
    });
    if (status === 200) return;
    if (Date.now() > deadline) {
      throw new Error(`daemon /health not ready within ${timeoutMs}ms`);
    }
    await new Promise((r) => setTimeout(r, 500));
  }
}

async function stopDaemon(p: ChildProcess): Promise<void> {
  const exited = new Promise<void>((resolve) => p.once("exit", () => resolve()));
  p.kill("SIGTERM");
  await exited;
}

test.beforeAll(async () => {
  const [admin, raft, grpc] = await Promise.all([
    findFreePort(),
    findFreePort(),
    findFreePort(),
  ]);
  ports = { admin, raft, grpc };
  dataDir = mkdtempSync(join(tmpdir(), "qrdb-idem-e2e-"));
  adminUrl = `https://127.0.0.1:${admin}`;

  // First boot: bootstrap PKI + admin user, then commit a KV entry.
  proc = startDaemon();
  await waitHealth(60_000);

  firstBootArtifacts = captureArtifacts();

  const userResp = await adminHttps("GET", "/users/by-username/admin");
  expect(userResp.status).toBe(200);
  firstBootAdmin = JSON.parse(userResp.text) as UserResponse;

  const putResp = await adminHttps(
    "PUT",
    `/kv/${encodeURIComponent(kvKey)}`,
    kvValue,
    "text/plain",
  );
  expect(putResp.status).toBe(204);

  // Clean shutdown, then second boot against the same data dir + ports.
  await stopDaemon(proc);
  proc = startDaemon();
  await waitHealth(60_000);
});

test.afterAll(async () => {
  if (proc && proc.exitCode === null) {
    await stopDaemon(proc);
  }
  if (dataDir) {
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  }
});

test.describe("Bootstrap idempotency (restart safety)", () => {
  test("restart does not regenerate bootstrap PKI artifacts", () => {
    const after = captureArtifacts();
    for (const name of ARTIFACTS) {
      expect(after[name].sha256, `${name} content changed across restart`).toBe(
        firstBootArtifacts[name].sha256,
      );
      expect(after[name].mtimeMs, `${name} mtime changed across restart`).toBe(
        firstBootArtifacts[name].mtimeMs,
      );
    }
  });

  test("restart does not recreate the admin user", async () => {
    const resp = await adminHttps("GET", "/users/by-username/admin");
    expect(resp.status).toBe(200);
    const admin = JSON.parse(resp.text) as UserResponse;
    expect(admin.id).toBe(firstBootAdmin.id);
    expect(admin.created_at).toBe(firstBootAdmin.created_at);
    expect(admin.tenant_id).toBe(firstBootAdmin.tenant_id);
    expect(admin.role_ids).toEqual(firstBootAdmin.role_ids);
    expect(admin.enabled).toBe(true);
  });

  test("committed KV state survives the restart", async () => {
    const resp = await adminHttps("GET", `/kv/${encodeURIComponent(kvKey)}`);
    expect(resp.status).toBe(200);
    expect(resp.text).toBe(kvValue);

    // Cleanup.
    const del = await adminHttps("DELETE", `/kv/${encodeURIComponent(kvKey)}`);
    expect(del.status).toBe(204);
  });
});
