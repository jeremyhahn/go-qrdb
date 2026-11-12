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

import { execFileSync, spawn, type ChildProcess } from "child_process";
import { generateKeyPairSync } from "crypto";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
// playwright.dom.config.ts sets it for the whole run.
import { PROXY_CLIENT_TLS_DIR_ENV } from "../../../playwright.tls.ts";

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
  /**
   * Boots the daemon with authentication on and makes the UI a tenant's
   * admin. The fixture runs the first-boot ceremony (keeping the generated
   * system admin credential), creates the tenant as that admin with a
   * first admin enrolled from a fresh key, and hands the tenant admin's
   * certificate and key to the Vite proxy (PROXY_CLIENT_TLS_DIR_ENV), which
   * presents them to the daemon on every API call. tlsClientAuth defaults to
   * "request" so the daemon asks for the certificate. Needs a seal other
   * than "none" (auth requires the seal subsystem).
   */
  tenantAdmin?: TenantAdminOptions;
  /**
   * Like tenantAdmin, then the tenant admin invites a plain user holding the
   * tenant's data role and a role listing and reading databases (no admin
   * role), who redeems the invitation with its own CSR;
   * the Vite proxy presents the user's certificate instead of the admin's.
   */
  tenantUser?: TenantUserOptions;
}

export interface TenantUserOptions extends TenantAdminOptions {
  /** The user's username; defaults to `${tenant}-user`. */
  userName?: string;
  /**
   * The tenant's built-in role the user holds (its only role); defaults to
   * "operator", the data role (kv, mvcc, leases, ...) with no admin permission.
   * A tenant admin holds no data permission, so it cannot grant a new role one.
   */
  role?: string;
}

export interface TenantAdminOptions {
  /** Tenant id (and name) to create. */
  tenant: string;
  /** The tenant's first admin. Default "<tenant>-admin". */
  username?: string;
  /** Tenant isolation type. Default "prefix". */
  isolationType?: "prefix" | "barrier" | "volume";
}

/** A client certificate and its private key, both PEM. */
export interface ClientCredential {
  certPem: string;
  keyPem: string;
}

export interface DomDaemon {
  adminUrl: string;
  dataDir: string;
  proc: ChildProcess;
  /** The ceremony's generated system admin, set with tenantAdmin. */
  systemAdmin?: ClientCredential;
  /** The tenant admin the Vite proxy presents, set with tenantAdmin. */
  tenantAdmin?: ClientCredential & { tenant: string; username: string };
  /** The plain tenant user the Vite proxy presents, set with tenantUser. */
  tenantUser?: ClientCredential & { tenant: string; username: string; roleIds: string[] };
}

function proxyClientFiles(): { cert: string; key: string } | null {
  const dir = process.env[PROXY_CLIENT_TLS_DIR_ENV];
  if (dir === undefined || dir === "") return null;
  return { cert: join(dir, "client.crt"), key: join(dir, "client.key") };
}

/** Removes the proxy's client certificate, so the UI is unauthenticated again. */
function clearProxyClientCert(): void {
  const files = proxyClientFiles();
  if (files === null) return;
  rmSync(files.cert, { force: true });
  rmSync(files.key, { force: true });
}

/** Has the Vite proxy present cred to the daemon from its next connection on. */
function setProxyClientCert(cred: ClientCredential): void {
  const files = proxyClientFiles();
  if (files === null) {
    throw new Error(`${PROXY_CLIENT_TLS_DIR_ENV} is unset: run the DOM tier through playwright.dom.config.ts`);
  }
  mkdirSync(dirname(files.cert), { recursive: true, mode: 0o700 });
  writeFileSync(files.key, cred.keyPem, { mode: 0o600 });
  writeFileSync(files.cert, cred.certPem, { mode: 0o600 });
}

/**
 * Calls the daemon's REST API directly (self-signed TLS), presenting cred as
 * the client certificate when given; returns the status and parsed body.
 */
export async function daemonRequest(
  adminUrl: string,
  method: string,
  path: string,
  body?: unknown,
  cred?: ClientCredential,
  extraHeaders: Record<string, string> = {},
): Promise<{ status: number; json: unknown; text: string }> {
  const https = await import("https");
  return new Promise((resolve, reject) => {
    const payload = body === undefined ? "" : JSON.stringify(body);
    const req = https.default.request(
      `${adminUrl}${path}`,
      {
        method,
        rejectUnauthorized: false,
        ...(cred !== undefined ? { cert: cred.certPem, key: cred.keyPem } : {}),
        headers: {
          ...extraHeaders,
          ...(payload ? { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(payload) } : {}),
        },
      },
      (res) => {
        let text = "";
        res.on("data", (c: Buffer) => (text += c.toString()));
        res.on("end", () => {
          let json: unknown = null;
          try {
            json = text ? JSON.parse(text) : null;
          } catch {
            json = null;
          }
          resolve({ status: res.statusCode ?? 0, json, text });
        });
      },
    );
    req.on("error", reject);
    req.end(payload);
  });
}

/** A fresh P-256 key (PKCS#8 PEM) and a PKCS#10 CSR for CN=cn over it. */
function freshKeyAndCSR(cn: string): { keyPem: string; csrPem: string } {
  const { privateKey } = generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const keyPem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
  const dir = mkdtempSync(join(tmpdir(), "qrdb-dom-csr-"));
  try {
    const keyPath = join(dir, "key.pem");
    writeFileSync(keyPath, keyPem, { mode: 0o600 });
    const csrPem = execFileSync("openssl", ["req", "-new", "-sha256", "-key", keyPath, "-subj", `/CN=${cn}`]).toString();
    return { keyPem, csrPem };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/**
 * Runs the first-boot ceremony, creates the tenant as the generated system
 * admin with a first admin enrolled from a fresh key, and returns both
 * credentials.
 */
async function provisionTenantAdmin(
  daemon: DomDaemon,
  opts: TenantAdminOptions,
): Promise<Required<Pick<DomDaemon, "systemAdmin" | "tenantAdmin">>> {
  const init = await daemonRequest(daemon.adminUrl, "POST", "/v1/ceremony/initialize", {
    so_pin: "so-pin-dom-e2e",
    user_pin: "user-pin-dom-e2e",
    admin_generate: true,
  });
  const ij = init.json as { admin_cert_pem?: string; admin_key_pem?: string } | null;
  if (init.status >= 300 || !ij?.admin_cert_pem || !ij.admin_key_pem) {
    throw new Error(`ceremony initialize returned ${init.status}: ${init.text.slice(0, 300)}`);
  }
  // The generated key is []byte on the wire: base64 of its PEM.
  const systemAdmin = {
    certPem: ij.admin_cert_pem,
    keyPem: Buffer.from(ij.admin_key_pem, "base64").toString(),
  };
  await waitForHealthy(daemon);

  const username = opts.username ?? `${opts.tenant}-admin`;
  const { keyPem, csrPem } = freshKeyAndCSR(username);
  const created = await daemonRequest(
    daemon.adminUrl,
    "POST",
    "/v1/tenants",
    {
      id: opts.tenant,
      name: opts.tenant,
      isolation_type: opts.isolationType ?? "prefix",
      admin_username: username,
      admin_csr_pem: csrPem,
    },
    systemAdmin,
  );
  const cj = created.json as { admin?: { cert_pem?: string } } | null;
  if (created.status >= 300 || !cj?.admin?.cert_pem) {
    throw new Error(`create tenant ${opts.tenant} returned ${created.status}: ${created.text.slice(0, 300)}`);
  }
  return {
    systemAdmin,
    tenantAdmin: { certPem: cj.admin.cert_pem, keyPem, tenant: opts.tenant, username },
  };
}

/**
 * provisionTenantAdmin, then as the tenant admin: one role with opts.permissions,
 * an invitation for a user holding only that role, and the user's own
 * redemption of it with a fresh CSR (the user's certificate is the result).
 */
async function provisionTenantUser(
  daemon: DomDaemon,
  opts: TenantUserOptions,
): Promise<Required<Pick<DomDaemon, "systemAdmin" | "tenantAdmin" | "tenantUser">>> {
  const admins = await provisionTenantAdmin(daemon, opts);
  const admin = admins.tenantAdmin;
  const roleName = opts.role ?? "operator";
  const roles = await daemonRequest(daemon.adminUrl, "GET", "/roles/list", undefined, admin);
  const role = ((roles.json as { roles?: { id: string; name: string }[] } | null)?.roles ?? []).find(
    (r) => r.name === roleName,
  );
  if (roles.status >= 300 || !role) {
    throw new Error(`role ${roleName} not in tenant ${opts.tenant} (${roles.status}): ${roles.text.slice(0, 300)}`);
  }
  // The operator role has no database permissions, so the user could not see
  // the database its data lives in; the tenant admin, who holds them, grants
  // listing and reading databases in a role of the tenant's own.
  const viewerName = `${opts.tenant}-databases`;
  const viewer = await daemonRequest(
    daemon.adminUrl,
    "POST",
    "/roles",
    { name: viewerName, tenant_id: opts.tenant, permissions: ["databases:list", "databases:read"] },
    admin,
  );
  const vr = viewer.json as { id?: string } | null;
  if (viewer.status >= 300 || !vr?.id) {
    throw new Error(`create role ${viewerName} returned ${viewer.status}: ${viewer.text.slice(0, 300)}`);
  }

  const username = opts.userName ?? `${opts.tenant}-user`;
  const invite = await daemonRequest(
    daemon.adminUrl,
    "POST",
    "/users/invite",
    { username, email: `${username}@example.test`, roleIDs: [role.id, vr.id] },
    admin,
  );
  const vj = invite.json as { token?: string } | null;
  if (invite.status >= 300 || !vj?.token) {
    throw new Error(`invite ${username} returned ${invite.status}: ${invite.text.slice(0, 300)}`);
  }

  const { keyPem, csrPem } = freshKeyAndCSR(username);
  const redeemed = await daemonRequest(daemon.adminUrl, "POST", "/invitations/redeem", {
    tenantID: opts.tenant,
    token: vj.token,
    csrPEM: csrPem,
  });
  const ej = redeemed.json as { certPEM?: string; cert_pem?: string } | null;
  const certPem = ej?.certPEM ?? ej?.cert_pem;
  if (redeemed.status >= 300 || !certPem) {
    throw new Error(`redeem invitation for ${username} returned ${redeemed.status}: ${redeemed.text.slice(0, 300)}`);
  }
  return {
    ...admins,
    tenantUser: { certPem, keyPem, tenant: opts.tenant, username, roleIds: [role.id, vr.id] },
  };
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
  if ((opts.tenantAdmin ?? opts.tenantUser) !== undefined && opts.seal === "none") {
    throw new Error('tenantAdmin needs auth, which needs a seal other than "none"');
  }
  // A credential a failed run left behind must not authenticate this one.
  clearProxyClientCert();
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
    `--auth-enabled=${(opts.tenantAdmin ?? opts.tenantUser) !== undefined}`,
    `--tls-client-auth=${opts.tlsClientAuth ?? ((opts.tenantAdmin ?? opts.tenantUser) !== undefined ? "request" : "none")}`,
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
  const daemon: DomDaemon = { adminUrl, dataDir, proc };
  if (opts.tenantUser !== undefined) {
    try {
      Object.assign(daemon, await provisionTenantUser(daemon, opts.tenantUser));
    } catch (err) {
      proc.kill("SIGTERM");
      throw new Error(`${err}\nstderr:\n${Buffer.concat(stderr).toString()}`);
    }
    setProxyClientCert(daemon.tenantUser!);
  } else if (opts.tenantAdmin !== undefined) {
    try {
      Object.assign(daemon, await provisionTenantAdmin(daemon, opts.tenantAdmin));
    } catch (err) {
      proc.kill("SIGTERM");
      throw new Error(`${err}\nstderr:\n${Buffer.concat(stderr).toString()}`);
    }
    setProxyClientCert(daemon.tenantAdmin!);
  }
  return daemon;
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
  clearProxyClientCert();
  // Wait for the daemon to exit: the next test boots on the same fixed port,
  // and a daemon still shutting down answers (or resets) its requests.
  const { proc } = daemon;
  if (proc.exitCode === null && proc.signalCode === null) {
    await new Promise<void>((resolve) => {
      const kill = setTimeout(() => proc.kill("SIGKILL"), 15_000);
      proc.once("exit", () => {
        clearTimeout(kill);
        resolve();
      });
      proc.kill("SIGTERM");
    });
  }
  try {
    rmSync(daemon.dataDir, { recursive: true, force: true });
  } catch {
    // best effort
  }
}
