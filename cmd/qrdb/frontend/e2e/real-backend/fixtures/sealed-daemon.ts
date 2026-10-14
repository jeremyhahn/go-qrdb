// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Sealed-daemon fixture variants.
 *
 * Spawns ephemeral qrdbd processes configured with the seal barrier enabled
 * (passphrase or Shamir strategy). Each suite gets its own isolated data
 * directory and ephemeral ports so parallel test files cannot collide.
 *
 * Key design decisions:
 * - Seal config is YAML-only (no CLI flags exist for it), so this fixture
 *   writes a temporary YAML config file before spawning the daemon.
 * - A restart() helper allows specs to verify persistence across restarts.
 * - dataDir and admin cert paths are exposed so specs can inspect bootstrap
 *   artifacts directly without going through the API.
 * - The default daemon fixture (daemon.ts) is NOT modified to preserve the
 *   existing 59 specs that run with auth-disabled and no seal barrier.
 */

import { test as base } from "@playwright/test";
import { spawn, type ChildProcess } from "child_process";
import {
  mkdtempSync,
  rmSync,
  mkdirSync,
  writeFileSync,
  promises as fsp,
} from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import {
  findFreePort,
  waitForHealthExported,
  waitForSealedWindow,
} from "./daemon-helpers.ts";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// ---------------------------------------------------------------------------
// Binary resolution (shared with daemon.ts)
// ---------------------------------------------------------------------------

function qrdbdBin(): string {
  return (
    process.env["QRDBD_BIN"] ??
    join(__dirname, "..", "..", "..", "..", "..", "..", "bin", "qrdbd")
  );
}

// ---------------------------------------------------------------------------
// Config generation
// ---------------------------------------------------------------------------

/** Options for a sealed-daemon variant. */
export interface SealedDaemonOptions {
  /** "passphrase" (default) or "shamir" */
  sealStrategy?: "passphrase" | "shamir";
  /** Minimum shares required to unseal. Only relevant when sealStrategy="shamir". */
  shamirThreshold?: number;
  /** Total number of Shamir shares to generate. Only relevant when sealStrategy="shamir". */
  shamirTotal?: number;
  /**
   * TLS client-auth mode passed as --tls-client-auth=<value>.
   * Defaults to "none" for the passphrase variant.
   * The shamir fixture sets this to "request" so that officers can present
   * their mTLS certificates during the claim flow. "request" asks the server
   * to request a client cert without CA-verifying it at the TLS handshake;
   * the handler's thumbprint binding (SHA-256 of the DER cert) is the actual
   * verification. Accepted values by qrdbd: none | request | require_and_verify.
   */
  tlsClientAuth?: "none" | "request" | "require_and_verify";
  /**
   * Enable per-request authentication (--auth-enabled=true).
   * When set, --tls-client-auth is OMITTED entirely so the auth-derived
   * default (VerifyClientCertIfGiven) applies: public endpoints stay
   * reachable without a client certificate while presented certs are
   * CA-verified at the TLS handshake. Officer certificates are signed by
   * the cluster CA (CeremonyCA = TLS bundle CA), so the share-claim mTLS
   * handshake passes CA verification and the handler's thumbprint binding
   * remains the per-officer authorization.
   */
  authEnabled?: boolean;
}

/**
 * Writes a minimal qrdbd YAML config file to configPath with the seal barrier
 * enabled. CLI flags for ports, data-dir, etc. override YAML when both are
 * present, so only the seal stanza needs to live in the file.
 *
 * A single-path volume pool rooted under the data directory is always
 * configured: per-tenant dedicated barriers allocate tenant stores from the
 * pool (RegisterTenantPooled), and without one POST
 * /v1/tenants/{id}/barrier/init/complete fails with ErrPoolNotConfigured (HTTP 500).
 */
function writeSealConfig(
  configPath: string,
  dataDir: string,
  opts: SealedDaemonOptions,
): void {
  const strategy = opts.sealStrategy ?? "passphrase";
  const threshold = opts.shamirThreshold ?? 2;
  const total = opts.shamirTotal ?? 3;

  const poolDir = join(dataDir, "pool", "p1");
  mkdirSync(poolDir, { recursive: true });

  // The codec section is required when using --config so that the codec
  // registry validation passes. The qrdbd binary is built without the
  // codec_msgpack build tag so the DefaultConfig's "msgpack" value fails
  // at runtime. Explicitly select "json" which is always registered.
  const lines = [
    "codec:",
    '  storage_codec: "json"',
    '  wire_codec: "json"',
    "seal:",
    "  enabled: true",
    `  strategy: "${strategy}"`,
  ];
  if (strategy === "shamir") {
    lines.push(`  shamir_threshold: ${threshold}`);
    lines.push(`  shamir_total: ${total}`);
  }
  lines.push(
    "storage:",
    "  volume_pools:",
    "    - name: default",
    `      paths: ["${poolDir}"]`,
    "      strategy: round_robin",
  );

  writeFileSync(configPath, lines.join("\n") + "\n", { mode: 0o600 });
}

// ---------------------------------------------------------------------------
// Spawn helpers
// ---------------------------------------------------------------------------

interface SpawnedDaemon {
  proc: ChildProcess;
  adminUrl: string;
  adminPort: number;
  raftPort: number;
  grpcPort: number;
  dataDir: string;
  configPath: string;
}

async function spawnSealedDaemon(
  dataDir: string,
  configPath: string,
  opts: SealedDaemonOptions,
): Promise<SpawnedDaemon> {
  const [adminPort, raftPort, grpcPort] = await Promise.all([
    findFreePort(),
    findFreePort(),
    findFreePort(),
  ]);

  writeSealConfig(configPath, dataDir, opts);

  const args = [
    "start",
    "--config",
    configPath,
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
    // Auth posture: the default sealed fixtures disable auth so barrier
    // operations do not require credentials (the barrier itself still
    // encrypts data). The auth-enabled variant omits --tls-client-auth so
    // the auth-derived VerifyClientCertIfGiven default applies (an explicit
    // value would override it — see applyTLSClientAuth in cmd/qrdbd).
    ...(opts.authEnabled
      ? ["--auth-enabled=true"]
      : [
          "--auth-enabled=false",
          `--tls-client-auth=${opts.tlsClientAuth ?? "none"}`,
        ]),
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

  const adminUrl = `https://127.0.0.1:${adminPort}`;

  proc.on("error", (err) => {
    console.error("[sealed-daemon] spawn error:", err);
  });

  // Collect stderr for diagnostics on health-check failure.
  const stderrChunks: Buffer[] = [];
  if (proc.stderr) {
    proc.stderr.on("data", (chunk: Buffer) => stderrChunks.push(chunk));
  }

  // Brief pause before polling — avoids hitting the port before the OS
  // completes the bind.
  await new Promise<void>((r) => setTimeout(r, 300));

  // A sealed daemon with an uninitialized barrier serves /v1/barrier/status
  // (sealed:true) while /health returns 503. Poll the sealed-window endpoint
  // rather than /health so that spawnSealedDaemon resolves as soon as the
  // daemon is ready for barrier operations (initialize / unseal).
  try {
    await waitForSealedWindow(adminUrl);
  } catch (err) {
    proc.kill("SIGTERM");
    await new Promise<void>((r) => setTimeout(r, 500));
    const stderr = Buffer.concat(stderrChunks).toString();
    throw new Error(
      `[sealed-daemon] sealed-window check failed.\nArgs: ${args.join(" ")}\nStderr:\n${stderr}\nOriginal: ${err}`,
    );
  }

  return { proc, adminUrl, adminPort, raftPort, grpcPort, dataDir, configPath };
}

async function killDaemon(proc: ChildProcess): Promise<void> {
  proc.kill("SIGTERM");
  await new Promise<void>((r) => setTimeout(r, 400));
}

// ---------------------------------------------------------------------------
// Public fixture types
// ---------------------------------------------------------------------------

/** Extended daemon info exposed to sealed-daemon specs. */
export interface SealedDaemonInfo {
  /** Full admin base URL, e.g. https://127.0.0.1:54321 */
  adminUrl: string;
  /** Ephemeral admin port */
  adminPort: number;
  /** Ephemeral Raft / listen port */
  raftPort: number;
  /** Ephemeral gRPC port */
  grpcPort: number;
  /** Temporary data directory (cleaned up after all tests) */
  dataDir: string;
  /** Path to admin-cert.pem (populated only when auth-enabled=true) */
  adminCertPath: string;
  /** Path to admin-key.pem (populated only when auth-enabled=true) */
  adminKeyPath: string;
  /**
   * Restart the daemon with the same data directory and YAML config.
   * Blocks until the health endpoint is ready again. Returns the new
   * admin URL (port may differ if the old port was reused).
   */
  restart(): Promise<void>;
}

export type SealedDaemonFixtures = {
  sealedDaemon: SealedDaemonInfo;
  shamirDaemon: SealedDaemonInfo;
  authShamirDaemon: SealedDaemonInfo;
};

// ---------------------------------------------------------------------------
// Internal state per fixture instance
// ---------------------------------------------------------------------------

/** Build a SealedDaemonInfo wrapper around a SpawnedDaemon. */
function makeSealedInfo(
  spawned: SpawnedDaemon,
  opts: SealedDaemonOptions,
  onRestarted: (s: SpawnedDaemon) => void,
): SealedDaemonInfo {
  let current = spawned;

  const info: SealedDaemonInfo = {
    get adminUrl() {
      return current.adminUrl;
    },
    get adminPort() {
      return current.adminPort;
    },
    get raftPort() {
      return current.raftPort;
    },
    get grpcPort() {
      return current.grpcPort;
    },
    get dataDir() {
      return current.dataDir;
    },
    adminCertPath: join(spawned.dataDir, "admin-cert.pem"),
    adminKeyPath: join(spawned.dataDir, "admin-key.pem"),
    restart: async () => {
      await killDaemon(current.proc);
      const next = await spawnSealedDaemon(
        current.dataDir,
        current.configPath,
        opts,
      );
      current = next;
      onRestarted(next);
    },
  };

  return info;
}

// ---------------------------------------------------------------------------
// Extended test with sealed daemon fixtures
// ---------------------------------------------------------------------------

export const test = base.extend<SealedDaemonFixtures>({
  /**
   * sealedDaemon: passphrase barrier, fresh per-test data dir.
   * The barrier is enabled but not yet initialized — tests call
   * POST /v1/barrier/initialize themselves so they control the secret.
   */
  sealedDaemon: async ({}, use) => {
    const dataDir = mkdtempSync(join(tmpdir(), "qrdb-seal-e2e-"));
    const configPath = join(dataDir, "seal-config.yaml");
    mkdirSync(dataDir, { recursive: true });

    let spawned = await spawnSealedDaemon(dataDir, configPath, {
      sealStrategy: "passphrase",
    });
    const info = makeSealedInfo(
      spawned,
      { sealStrategy: "passphrase" },
      (next) => {
        spawned = next;
      },
    );

    await use(info);

    await killDaemon(spawned.proc);
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  },

  /**
   * shamirDaemon: Shamir 3-of-5 barrier, fresh per-test data dir.
   */
  shamirDaemon: async ({}, use) => {
    const dataDir = mkdtempSync(join(tmpdir(), "qrdb-shamir-e2e-"));
    const configPath = join(dataDir, "seal-config.yaml");
    mkdirSync(dataDir, { recursive: true });

    const opts: SealedDaemonOptions = {
      sealStrategy: "shamir",
      shamirThreshold: 3,
      shamirTotal: 5,
      // "request" causes the TLS server to ask for a client certificate
      // without CA-verifying it at the handshake. The officer claim handlers
      // perform the actual verification via SHA-256 thumbprint matching
      // against the registered officer's PersistentOfficer.CertThumbprint.
      // This is required for the share-claim flow (INV-4, INV-12).
      tlsClientAuth: "request",
    };

    let spawned = await spawnSealedDaemon(dataDir, configPath, opts);
    const info = makeSealedInfo(spawned, opts, (next) => {
      spawned = next;
    });

    await use(info);

    await killDaemon(spawned.proc);
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  },

  /**
   * authShamirDaemon: Shamir 3-of-5 barrier with --auth-enabled=true.
   *
   * The auth-derived TLS posture (VerifyClientCertIfGiven) applies because
   * --tls-client-auth is omitted. This is the fixture that e2e-exercises
   * the identity-gated officer claim authorization (permission.IdentityGated):
   * the full post-unseal server runs the real auth middleware, so officer
   * claims must succeed with only the enrolled mTLS identity while
   * RBAC-gated routes reject anonymous callers.
   */
  authShamirDaemon: async ({}, use) => {
    const dataDir = mkdtempSync(join(tmpdir(), "qrdb-authshamir-e2e-"));
    const configPath = join(dataDir, "seal-config.yaml");
    mkdirSync(dataDir, { recursive: true });

    const opts: SealedDaemonOptions = {
      sealStrategy: "shamir",
      shamirThreshold: 3,
      shamirTotal: 5,
      authEnabled: true,
    };

    let spawned = await spawnSealedDaemon(dataDir, configPath, opts);
    const info = makeSealedInfo(spawned, opts, (next) => {
      spawned = next;
    });

    await use(info);

    await killDaemon(spawned.proc);
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  },
});

export { expect } from "@playwright/test";

// Re-export file system helpers for specs that read bootstrap artifacts.
export { fsp };
