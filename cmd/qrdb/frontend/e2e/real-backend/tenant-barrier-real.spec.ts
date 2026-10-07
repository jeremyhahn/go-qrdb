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
 *   - strategy "shamir" splits the tenant's unseal key to the tenant's
 *     enrolled share holders: with none enrolled the init is refused; with
 *     them enrolled it is share-free, each holder claims its envelope with the
 *     qrdb CLI, and a quorum of them completes a rekey or a revocation.
 *
 * Secrets are []byte on the server so they can be zeroized: base64 in JSON.
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
import { b64, findFreePort, waitForSealedWindow, waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import { enrolHolders, mustClaim, newHolder, quorum, submitShare } from "./fixtures/officer-bootstrap.ts";

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
      "--auth-enabled=false", "--tls-client-auth=request", "--discovery", "static",
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
  const init = await https("POST", `${adminUrl}/v1/barrier/initialize`, { secret: b64("system-secret") });
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
      passphrase: b64("acme-tenant-secret"),
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

  test("per-tenant Shamir init without enrolled holders is refused, share-free", async () => {
    await https("POST", `${daemon.adminUrl}/v1/tenants`, { id: "gamma", name: "Gamma", isolation_type: "prefix" });
    const init = await https("POST", `${daemon.adminUrl}/v1/tenants/gamma/barrier/init`, {
      threshold: 2,
      shares: 3,
      strategy: "shamir",
    });
    expect(init.status).toBeGreaterThanOrEqual(400);
    expect(JSON.stringify(init.json ?? "")).not.toMatch(/share_bytes|envelope/);
    expect(init.json?.shares ?? null).toBeNull();
  });

  test("per-tenant Shamir with enrolled holders: share-free init, CLI claims, quorum rekey and revocation", async () => {
    const tenantId = "delta";
    const scope = { tenantId };
    await https("POST", `${daemon.adminUrl}/v1/tenants`, { id: tenantId, name: "Delta", isolation_type: "prefix" });

    const holders = ["t-a", "t-b", "t-c"].map((n, i) => newHolder(n, i === 0 ? "so" : "custodian"));
    const { request: apiRequest } = await import("@playwright/test");
    const req = await apiRequest.newContext({ ignoreHTTPSErrors: true });
    try {
      await enrolHolders(req, daemon.adminUrl, holders, scope);
    } finally {
      await req.dispose();
    }

    const init = await https("POST", `${daemon.adminUrl}/v1/tenants/${tenantId}/barrier/init`, {
      threshold: 2,
      shares: 3,
      strategy: "shamir",
    });
    expect(init.status).toBe(200);
    expect(init.json).toMatchObject({ tenant_id: tenantId, strategy: "shamir", threshold: 2, total_shares: 3 });
    expect(JSON.stringify(init.json)).not.toMatch(/share_bytes|envelope/);

    for (const h of holders) mustClaim(daemon, h, scope);

    // A tenant rekey needs RBAC (no SO PIN) and a quorum of current holders.
    const rekey = await https("POST", `${daemon.adminUrl}/v1/tenants/${tenantId}/barrier/rekey/start`, { threshold: 2 });
    expect(rekey.status).toBe(200);
    expect(quorum(daemon, holders, 2, "rekey", scope)).toMatchObject({ complete: true, share_count: 3 });
    for (const h of holders) mustClaim(daemon, h, scope);

    // Revoking t-c: the remaining two complete it, and t-c cannot take part.
    const revoke = await https("POST", `${daemon.adminUrl}/v1/tenants/${tenantId}/barrier/officers/revoke`, {
      username: "t-c",
    });
    expect(revoke.status).toBe(200);
    expect(revoke.json.remove).toEqual(["t-c"]);
    expect(submitShare(daemon, holders[2]!, "rekey", scope).status).not.toBe(0);
    expect(quorum(daemon, holders, 2, "rekey", scope)).toMatchObject({ complete: true, share_count: 2 });
  });

  test("per-database Shamir: created sealed with no secret, share-free init, CLI claims, quorum seal and unseal, rekey", async () => {
    const database = "vault";
    const scope = { database };
    // The create the web UI's wizard sends for a Shamir barrier.
    const create = await https("POST", `${daemon.adminUrl}/v1/databases`, {
      name: database,
      backend: "file",
      dataDir: join(daemon.dataDir, "databases-e2e", database),
      barrier: { enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 2 },
    });
    expect(create.status).toBe(201);
    expect(create.json).toMatchObject({ name: database, status: "sealed", barrier: { enabled: true, mode: "shamir" } });

    // A Shamir barrier takes no secret.
    const withSecret = await https("POST", `${daemon.adminUrl}/v1/databases`, {
      name: "vault-secret",
      backend: "file",
      dataDir: join(daemon.dataDir, "databases-e2e", "vault-secret"),
      barrier: { enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 2, secret: "x" },
    });
    expect(withSecret.status).toBe(400);

    const holders = ["d-a", "d-b"].map((n, i) => newHolder(n, i === 0 ? "so" : "custodian"));
    const { request: apiRequest } = await import("@playwright/test");
    const req = await apiRequest.newContext({ ignoreHTTPSErrors: true });
    try {
      await enrolHolders(req, daemon.adminUrl, holders, scope);
    } finally {
      await req.dispose();
    }
    const listed = await https("GET", `${daemon.adminUrl}/v1/databases/${database}/barrier/officers`);
    expect(listed.status).toBe(200);
    expect(listed.json.officers.map((o: { username: string }) => o.username).sort()).toEqual(["d-a", "d-b"]);

    const init = await https("POST", `${daemon.adminUrl}/v1/databases/${database}/barrier/shamir/initialize`, {});
    expect(init.status).toBe(200);
    expect(init.json).toEqual({ threshold: 2, total_shares: 2, share_count: 2 });

    for (const h of holders) mustClaim(daemon, h, scope);

    // A single caller cannot seal it; a quorum of its holders does.
    const single = await https("POST", `${daemon.adminUrl}/v1/databases/${database}/seal`, {});
    expect(single.status).toBe(422);
    expect(quorum(daemon, holders, 2, "seal", scope)).toMatchObject({ complete: true });
    expect((await https("GET", `${daemon.adminUrl}/v1/databases/${database}/barrier/status`)).json.sealed).toBe(true);
    expect(quorum(daemon, holders, 2, "unseal", scope)).toMatchObject({ complete: true });
    expect((await https("GET", `${daemon.adminUrl}/v1/databases/${database}/barrier/status`)).json.sealed).toBe(false);

    const rekey = await https("POST", `${daemon.adminUrl}/v1/databases/${database}/barrier/rekey/start`, { threshold: 2 });
    expect(rekey.status).toBe(200);
    expect(quorum(daemon, holders, 2, "rekey", scope)).toMatchObject({ complete: true, share_count: 2 });
  });
});
