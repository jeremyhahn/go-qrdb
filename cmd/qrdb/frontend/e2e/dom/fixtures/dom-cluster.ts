// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * dom-cluster.ts
 *
 * Boots a real 3-node qrdbd Raft cluster for the DOM test tier so the browser
 * can drive the cluster-only UI surfaces (Cluster / Topology nav, the database
 * wizard's replication-factor picker) against a live multi-node backend.
 *
 * Node 1 owns the fixed DOM admin port (63012) that the Vite dev server proxies
 * to (QRDB_PROXY_TARGET in playwright.dom.config.ts), so the UI talks to node 1
 * while all three nodes form the system Raft group (shard 1, replicas 1-3).
 * Reads (topology, list) are served locally; writes are leader-forwarded.
 *
 * Inter-node Raft transport is mutually authenticated TLS, so all three nodes
 * are issued leaf certificates from a single shared CA (generated here with
 * openssl). The barrier (seal) subsystem is disabled — this fixture exercises
 * cluster topology + placement UI, not the per-tenant key-delivery path, so no
 * SPIFFE node identity or unseal ceremony is required.
 *
 * The fixture runs single-worker (the DOM config pins workers: 1) because the
 * nodes own fixed ports.
 */

import { spawn, execFileSync, type ChildProcess } from "child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import { DOM_ADMIN_PORT, UI_BASE_URL } from "./dom-daemon.ts";

export { UI_BASE_URL };

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

/** Resolves the qrdbd binary (QRDBD_BIN overrides; falls back to bin/qrdbd). */
function qrdbdBin(): string {
  return (
    process.env["QRDBD_BIN"] ??
    join(__dirname, "..", "..", "..", "..", "..", "..", "bin", "qrdbd")
  );
}

// Per-node fixed ports. Node 1's admin port MUST equal DOM_ADMIN_PORT so the
// Vite proxy reaches it. Raft/admin/grpc are spaced by 10 per node.
interface NodePorts {
  raft: number;
  admin: number;
  grpc: number;
}
const NODE_PORTS: readonly NodePorts[] = [
  { raft: 63011, admin: DOM_ADMIN_PORT, grpc: 63013 },
  { raft: 63021, admin: 63022, grpc: 63023 },
  { raft: 63031, admin: 63032, grpc: 63033 },
];

const CLUSTER_TRUST_DOMAIN = "qrdb.cluster";

export interface DomCluster {
  /** Admin URL of node 1 (the proxy target). */
  adminUrl: string;
  /** Admin URL of every node, node 1 first. */
  adminUrls: string[];
  /** Number of nodes in the cluster. */
  nodeCount: number;
  procs: ChildProcess[];
  dataDirs: string[];
  certDir: string;
}

/**
 * Generates a shared CA and one leaf certificate per node using openssl. Each
 * leaf carries localhost / loopback SANs and server+client extended key usage,
 * mirroring the daemon's own auto-generated node certificates so the Raft QUIC
 * transport accepts every peer.
 */
function generateClusterCerts(
  certDir: string,
  n: number,
): {
  caPath: string;
  nodes: { certPath: string; keyPath: string }[];
  admin: { certPath: string; keyPath: string };
} {
  const caKey = join(certDir, "ca.key");
  const caPath = join(certDir, "ca.crt");

  execFileSync("openssl", [
    "ecparam",
    "-genkey",
    "-name",
    "prime256v1",
    "-out",
    caKey,
  ]);
  execFileSync("openssl", [
    "req",
    "-x509",
    "-new",
    "-key",
    caKey,
    "-days",
    "1",
    "-out",
    caPath,
    "-subj",
    "/CN=qrdb-dom-cluster-ca",
    "-addext",
    "basicConstraints=critical,CA:TRUE",
    "-addext",
    "keyUsage=critical,keyCertSign,cRLSign",
  ]);

  // SAN/EKU extension file shared by all leaves.
  const extPath = join(certDir, "leaf.ext");
  writeFileSync(
    extPath,
    [
      "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:0.0.0.0,IP:::1",
      "extendedKeyUsage=serverAuth,clientAuth",
      "keyUsage=digitalSignature,keyEncipherment",
      "basicConstraints=CA:FALSE",
      "",
    ].join("\n"),
  );

  // The admin leaf names its tenant and user in a SPIFFE URI SAN
  // (spiffe://<tenant>/user/<name>), matching its first OU.
  const adminExtPath = join(certDir, "admin.ext");
  writeFileSync(
    adminExtPath,
    [
      "subjectAltName=URI:spiffe://system/user/admin",
      "extendedKeyUsage=clientAuth",
      "keyUsage=digitalSignature",
      "basicConstraints=CA:FALSE",
      "",
    ].join("\n"),
  );

  const nodes: { certPath: string; keyPath: string }[] = [];
  // n node leaves, then one more leaf (index n) for the cluster admin's
  // bring-your-own certificate.
  for (let i = 0; i <= n; i++) {
    const name = i < n ? `node${i + 1}` : "admin";
    const keyPath = join(certDir, `${name}.key`);
    const csrPath = join(certDir, `${name}.csr`);
    const certPath = join(certDir, `${name}.crt`);
    execFileSync("openssl", [
      "ecparam",
      "-genkey",
      "-name",
      "prime256v1",
      "-out",
      keyPath,
    ]);
    execFileSync("openssl", [
      "req",
      "-new",
      "-key",
      keyPath,
      "-out",
      csrPath,
      "-subj",
      i < n ? `/CN=qrdb-node-${i + 1}` : "/OU=system/CN=admin",
    ]);
    execFileSync("openssl", [
      "x509",
      "-req",
      "-in",
      csrPath,
      "-CA",
      caPath,
      "-CAkey",
      caKey,
      "-CAcreateserial",
      "-days",
      "1",
      "-out",
      certPath,
      "-extfile",
      i < n ? extPath : adminExtPath,
    ]);
    nodes.push({ certPath, keyPath });
  }
  const admin = nodes.pop()!;
  return { caPath, nodes, admin };
}

/** Seal modes the cluster fixture boots. */
export type ClusterSealMode = "none" | "passphrase";

export interface StartClusterOptions {
  /**
   * "none" (default): seal and auth off. "passphrase": every node runs a
   * passphrase barrier; the cluster is initialized through POST
   * /v1/ceremony/initialize on node 1 with the PASSPHRASE / SO_PIN /
   * USER_PIN below.
   */
  seal?: ClusterSealMode;
  /** "mvcc": every node runs the MVCC state machine, which keeps the
   * revision history the node compaction policy needs. */
  sm?: "mvcc";
}

/** Credentials a passphrase-sealed cluster is initialized with. */
export const CLUSTER_PASSPHRASE = "dom-cluster-active-seal-secret-1";
export const CLUSTER_SO_PIN = "dom-cluster-active-seal-so-pin";
export const CLUSTER_USER_PIN = "dom-cluster-active-seal-user-pin";

function writeClusterConfig(path: string, seal: ClusterSealMode): void {
  const lines = [
    "codec:",
    '  storage_codec: "json"',
    '  wire_codec: "json"',
    "seal:",
  ];
  if (seal === "none") {
    lines.push("  enabled: false");
  } else {
    lines.push("  enabled: true", `  strategy: "${seal}"`);
  }
  // auth off in every mode: the DOM tier drives the UI with no login (a
  // seal-off server cannot hold credentials at all), and the seal and
  // topology surfaces under test need none.
  lines.push("auth:", "  enabled: false");
  writeFileSync(path, lines.join("\n") + "\n", { mode: 0o600 });
}

async function httpsGetStatus(
  url: string,
  timeoutMs = 1000,
): Promise<{ code: number; body: string } | null> {
  const https = await import("https");
  return new Promise((resolve) => {
    const req = https.default.get(url, { rejectUnauthorized: false }, (res) => {
      const chunks: Buffer[] = [];
      res.on("data", (c: Buffer) => chunks.push(c));
      res.on("end", () =>
        resolve({
          code: res.statusCode ?? 0,
          body: Buffer.concat(chunks).toString(),
        }),
      );
    });
    req.on("error", () => resolve(null));
    req.setTimeout(timeoutMs, () => {
      req.destroy();
      resolve(null);
    });
  });
}

async function httpsPostJson(
  url: string,
  body: unknown,
  timeoutMs = 30_000,
): Promise<{ code: number; body: string }> {
  const https = await import("https");
  const payload = JSON.stringify(body);
  return new Promise((resolve, reject) => {
    const req = https.default.request(
      url,
      {
        method: "POST",
        rejectUnauthorized: false,
        headers: {
          "Content-Type": "application/json",
          "Content-Length": Buffer.byteLength(payload),
        },
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () =>
          resolve({
            code: res.statusCode ?? 0,
            body: Buffer.concat(chunks).toString(),
          }),
        );
      },
    );
    req.on("error", reject);
    req.setTimeout(timeoutMs, () => {
      req.destroy(new Error(`POST ${url} timed out`));
    });
    req.end(payload);
  });
}

/** Polls url until it answers 200. */
async function waitForStatus200(
  url: string,
  timeoutMs = 30_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let last = "no response";
  while (Date.now() < deadline) {
    const res = await httpsGetStatus(url);
    if (res !== null && res.code === 200) return;
    last = res === null ? "unreachable" : `status ${res.code} ${res.body}`;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`${url} not 200 within ${timeoutMs}ms (last: ${last})`);
}

/**
 * Initializes every node's passphrase barrier through POST
 * /v1/ceremony/initialize (each node holds its own barrier key; the cluster
 * seal record is what the nodes share).
 */
async function initializeCluster(
  adminUrls: string[],
  adminCertPem: string,
): Promise<void> {
  for (const url of adminUrls) {
    await waitForStatus200(`${url}/v1/barrier/status`);
  }
  for (const url of adminUrls) {
    const res = await httpsPostJson(`${url}/v1/ceremony/initialize`, {
      secret: Buffer.from(CLUSTER_PASSPHRASE).toString("base64"),
      so_pin: CLUSTER_SO_PIN,
      user_pin: CLUSTER_USER_PIN,
      admin_cert_pem: adminCertPem,
    });
    if (res.code < 200 || res.code >= 300) {
      throw new Error(`ceremony init ${url}: ${res.code} ${res.body}`);
    }
  }
  // A node confirms the cluster seal records only after it has read them:
  // until then it answers 503 SEAL_STATE_UNCONFIRMED. Wait for every node to
  // serve so specs start from a settled cluster.
  for (const url of adminUrls) {
    const deadline = Date.now() + 60_000;
    let last: string;
    for (;;) {
      const res = await httpsGetStatus(`${url}/v1/barrier/status`);
      if (
        res !== null &&
        res.code === 200 &&
        res.body.includes('"local":"serving"')
      )
        break;
      last = res === null ? "unreachable" : `${res.code} ${res.body}`;
      if (Date.now() > deadline) {
        throw new Error(`${url} not serving within 60000ms (last: ${last})`);
      }
      await new Promise((r) => setTimeout(r, 250));
    }
  }
}

/**
 * Polls node 1's /admin/topology until the system Raft group reports the full
 * member set (membership converged + a leader elected). Returns the observed
 * distinct node count.
 */
async function waitForClusterTopology(
  adminUrl: string,
  want: number,
  timeoutMs = 60_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let last = "no response";
  while (Date.now() < deadline) {
    const res = await httpsGetStatus(`${adminUrl}/admin/topology`);
    if (res !== null && res.code === 200) {
      try {
        const topo = JSON.parse(res.body) as {
          shards?: { nodes?: Record<string, string> }[];
        };
        const ids = new Set<string>();
        for (const shard of topo.shards ?? []) {
          for (const id of Object.keys(shard.nodes ?? {})) ids.add(id);
        }
        if (ids.size >= want) return;
        last = `topology has ${ids.size}/${want} nodes`;
      } catch (e) {
        last = `bad topology json: ${String(e)}`;
      }
    } else {
      last = res === null ? "unreachable" : `status ${res.code}`;
    }
    await new Promise((r) => setTimeout(r, 400));
  }
  throw new Error(
    `cluster topology did not reach ${want} nodes within ${timeoutMs}ms (last: ${last})`,
  );
}

/** Boots a 3-node cluster and blocks until topology reports all members. */
export async function startCluster(
  nodeCount = 3,
  opts: StartClusterOptions = {},
): Promise<DomCluster> {
  const seal = opts.seal ?? "none";
  const certDir = mkdtempSync(join(tmpdir(), "qrdb-dom-cluster-certs-"));
  const {
    caPath,
    nodes: nodeCerts,
    admin: adminCert,
  } = generateClusterCerts(certDir, nodeCount);

  const peersFlag = NODE_PORTS.slice(0, nodeCount)
    .map((p, i) => `${i + 1}=127.0.0.1:${p.raft}`)
    .join(",");
  // Static peers must each list every replica's Raft member identity
  // (spki:<sha256-hex>), which qrdbd node-identity derives from the cert.
  const identitiesFlag = nodeCerts
    .map((c, i) => {
      const id = execFileSync(qrdbdBin(), [
        "node-identity",
        "--cert",
        c.certPath,
      ])
        .toString()
        .trim();
      return `${i + 1}=${id}`;
    })
    .join(",");

  const procs: ChildProcess[] = [];
  const dataDirs: string[] = [];
  const stderrBufs: Buffer[][] = [];

  for (let i = 0; i < nodeCount; i++) {
    const ports = NODE_PORTS[i]!;
    const dataDir = mkdtempSync(join(tmpdir(), `qrdb-dom-cluster-n${i + 1}-`));
    dataDirs.push(dataDir);
    const configPath = join(dataDir, "config.yaml");
    writeClusterConfig(configPath, seal);

    const args = [
      "start",
      "--config",
      configPath,
      "--data-dir",
      dataDir,
      "--listen",
      `0.0.0.0:${ports.raft}`,
      "--raft-address",
      `127.0.0.1:${ports.raft}`,
      "--admin-port",
      String(ports.admin),
      "--grpc-port",
      String(ports.grpc),
      "--tls-client-auth=none",
      "--discovery",
      "static",
      "--peers",
      peersFlag,
      "--member-identities",
      identitiesFlag,
      "--shard-id",
      "1",
      "--replica-id",
      String(i + 1),
      "--cluster-trust-domain",
      CLUSTER_TRUST_DOMAIN,
      "--tls-ca-file",
      caPath,
      "--tls-cert-file",
      nodeCerts[i]!.certPath,
      "--tls-key-file",
      nodeCerts[i]!.keyPath,
      "--log-level",
      "warn",
      "--enable-grpc=false",
      "--enable-unix=false",
      "--enable-quic=false",
      "--enable-mcp=false",
      "--auth-enabled=false",
      // mvcc keeps revision history, which the node compaction policy needs.
      ...(opts.sm !== undefined ? ["--sm", opts.sm] : []),
    ];

    const proc = spawn(qrdbdBin(), args, {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, NO_COLOR: "1" },
    });
    const buf: Buffer[] = [];
    stderrBufs.push(buf);
    proc.stderr?.on("data", (c: Buffer) => buf.push(c));
    procs.push(proc);
  }

  const adminUrl = `https://127.0.0.1:${DOM_ADMIN_PORT}`;
  const adminUrls = NODE_PORTS.slice(0, nodeCount).map(
    (p) => `https://127.0.0.1:${p.admin}`,
  );
  try {
    if (seal !== "none") {
      await initializeCluster(
        adminUrls,
        readFileSync(adminCert.certPath, "utf8"),
      );
    }
    await waitForClusterTopology(adminUrl, nodeCount);
  } catch (err) {
    for (const p of procs) p.kill("SIGTERM");
    const logs = stderrBufs
      .map(
        (b, i) => `--- node${i + 1} stderr ---\n${Buffer.concat(b).toString()}`,
      )
      .join("\n");
    throw new Error(`${err}\n${logs}`, { cause: err });
  }

  return { adminUrl, adminUrls, nodeCount, procs, dataDirs, certDir };
}

/** Stops all cluster nodes and removes their data + cert directories. */
export async function stopCluster(cluster: DomCluster | null): Promise<void> {
  if (cluster === null) return;
  // Wait for every node to exit so the next fixture can bind the fixed
  // ports; a node still running after 15 s is killed.
  await Promise.all(
    cluster.procs.map(
      (p) =>
        new Promise<void>((resolve) => {
          if (p.exitCode !== null || p.signalCode !== null) {
            resolve();
            return;
          }
          const timer = setTimeout(() => p.kill("SIGKILL"), 15_000);
          p.once("exit", () => {
            clearTimeout(timer);
            resolve();
          });
          p.kill("SIGTERM");
        }),
    ),
  );
  for (const dir of [...cluster.dataDirs, cluster.certDir]) {
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  }
}
