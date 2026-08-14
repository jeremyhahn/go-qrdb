// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * tenant-barrier-real.spec.ts
 *
 * End-to-end per-tenant encryption barrier over REST against a live daemon
 * configured with a storage volume pool (a prerequisite for per-tenant
 * barriers). Guards the backend contract the TenantManager UI relies on:
 *
 *   - POST /v1/tenants/{id}/barrier/init {passphrase} -> 204, status initialized.
 *   - The same call WITHOUT a passphrase -> 400 "empty passphrase" (the bug the
 *     UI fix addresses: the form now collects a passphrase).
 *   - strategy "shamir" is supported but share-free (INV-1): init requires
 *     enrolled Security Officers so shares can be sealed to their enclaves.
 *     With no officers registered the init is rejected and no share material
 *     ever appears in the response.
 *
 * The daemon is spawned with seal enabled + a volume pool; the system barrier
 * is initialized via REST before tenants are created.
 */

import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import { findFreePort, waitForSealedWindow, waitForHealthExported } from "./fixtures/daemon-helpers.ts";

const __dirname = dirname(fileURLToPath(import.meta.url));

function qrdbdBin(): string {
  return process.env["QRDBD_BIN"] ?? join(__dirname, "..", "..", "..", "..", "..", "bin", "qrdbd");
}

interface Daemon {
  proc: ChildProcess;
  adminUrl: string;
  dataDir: string;
}

let daemon: Daemon;

async function https(method: string, url: string, body?: unknown): Promise<{ status: number; json: any }> {
  const mod = await import("https");
  const data = body !== undefined ? JSON.stringify(body) : undefined;
  return await new Promise((resolve, reject) => {
    const req = mod.default.request(
      url,
      { method, rejectUnauthorized: false, headers: data ? { "Content-Type": "application/json" } : {} },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString();
          let parsed: unknown = null;
          try {
            parsed = text ? JSON.parse(text) : null;
          } catch {
            parsed = text;
          }
          resolve({ status: res.statusCode ?? 0, json: parsed });
        });
      },
    );
    req.on("error", reject);
    if (data) req.write(data);
    req.end();
  });
}

test.beforeAll(async () => {
  const [adminPort, raftPort, grpcPort] = await Promise.all([findFreePort(), findFreePort(), findFreePort()]);
  const dataDir = mkdtempSync(join(tmpdir(), "qrdb-tnt-real-"));
  const poolDir = join(dataDir, "pool", "p1");
  mkdirSync(poolDir, { recursive: true });
  const configPath = join(dataDir, "config.yaml");
  writeFileSync(
    configPath,
    [
      "codec:",
      '  storage_codec: "json"',
      '  wire_codec: "json"',
      "seal:",
      "  enabled: true",
      '  strategy: "passphrase"',
      "storage:",
      "  volume_pools:",
      "    - name: default",
      `      paths: ["${poolDir}"]`,
      "      strategy: round_robin",
      "",
    ].join("\n"),
    { mode: 0o600 },
  );

  const proc = spawn(
    qrdbdBin(),
    [
      "start", "--config", configPath, "--data-dir", dataDir,
      "--listen", `127.0.0.1:${raftPort}`, "--raft-address", `127.0.0.1:${raftPort}`,
      "--admin-port", String(adminPort), "--grpc-port", String(grpcPort),
      "--auth-enabled=false", "--tls-client-auth=none", "--discovery", "static",
      "--peers", `1=127.0.0.1:${raftPort}`, "--replica-id", "1", "--log-level", "warn",
      "--enable-grpc=false", "--enable-unix=false", "--enable-quic=false", "--enable-mcp=false",
    ],
    { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, NO_COLOR: "1" } },
  );
  const adminUrl = `https://127.0.0.1:${adminPort}`;
  daemon = { proc, adminUrl, dataDir };

  await new Promise((r) => setTimeout(r, 300));
  await waitForSealedWindow(adminUrl);
  // Initialize the system barrier, then wait for full bootstrap.
  const init = await https("POST", `${adminUrl}/v1/barrier/initialize`, { secret: "system-secret" });
  expect(init.status).toBe(204);
  await waitForHealthExported(adminUrl, 30_000);
});

test.afterAll(async () => {
  if (daemon?.proc) {
    daemon.proc.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 400));
  }
  if (daemon?.dataDir) {
    try {
      rmSync(daemon.dataDir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  }
});

test.describe("Per-tenant barrier (passphrase, volume-pool backed)", () => {
  test("init with passphrase succeeds and reports initialized", async () => {
    const create = await https("POST", `${daemon.adminUrl}/v1/tenants`, {
      id: "acme",
      name: "Acme",
      isolation_type: "prefix",
    });
    expect(create.status).toBe(201);

    const init = await https("POST", `${daemon.adminUrl}/v1/tenants/acme/barrier/init`, {
      passphrase: "acme-tenant-secret",
      strategy: "passphrase",
    });
    expect(init.status).toBe(204);

    const status = await https("GET", `${daemon.adminUrl}/v1/tenants/acme/barrier/status`);
    expect(status.status).toBe(200);
    expect(status.json.initialized).toBe(true);
    expect(status.json.sealed).toBe(false);
    expect(status.json.strategy).toBe("passphrase");
  });

  test("init without a passphrase is rejected (400 empty passphrase)", async () => {
    await https("POST", `${daemon.adminUrl}/v1/tenants`, { id: "beta", name: "Beta", isolation_type: "prefix" });
    const init = await https("POST", `${daemon.adminUrl}/v1/tenants/beta/barrier/init`, { threshold: 3, shares: 5 });
    expect(init.status).toBe(400);
    expect(String(init.json?.error ?? "").toLowerCase()).toContain("passphrase");
  });

  test("per-tenant Shamir init without enrolled officers is rejected, share-free", async () => {
    await https("POST", `${daemon.adminUrl}/v1/tenants`, { id: "gamma", name: "Gamma", isolation_type: "prefix" });
    const init = await https("POST", `${daemon.adminUrl}/v1/tenants/gamma/barrier/init`, {
      threshold: 3,
      shares: 5,
      strategy: "shamir",
    });
    // Per-tenant Shamir is supported, but shares are sealed to enrolled
    // Security Officers (INV-1) — with none registered, init must fail.
    expect(init.status).toBeGreaterThanOrEqual(400);
    expect(String(init.json?.error ?? "").toLowerCase()).toMatch(/officer|distributor/);
    // INV-1: no share material may ever appear in an init response.
    expect(JSON.stringify(init.json ?? "")).not.toContain("share_bytes");
    expect(init.json?.shares ?? null).toBeNull();
  });
});
