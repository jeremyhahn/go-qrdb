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
import { mkdtempSync, rmSync, writeFileSync } from "fs";
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
function generateClusterCerts(certDir: string, n: number): {
  caPath: string;
  nodes: { certPath: string; keyPath: string }[];
} {
  const caKey = join(certDir, "ca.key");
  const caPath = join(certDir, "ca.crt");

  execFileSync("openssl", ["ecparam", "-genkey", "-name", "prime256v1", "-out", caKey]);
  execFileSync("openssl", [
    "req", "-x509", "-new", "-key", caKey, "-days", "1", "-out", caPath,
    "-subj", "/CN=qrdb-dom-cluster-ca",
    "-addext", "basicConstraints=critical,CA:TRUE",
    "-addext", "keyUsage=critical,keyCertSign,cRLSign",
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

  const nodes: { certPath: string; keyPath: string }[] = [];
  for (let i = 0; i < n; i++) {
    const keyPath = join(certDir, `node${i + 1}.key`);
    const csrPath = join(certDir, `node${i + 1}.csr`);
    const certPath = join(certDir, `node${i + 1}.crt`);
    execFileSync("openssl", ["ecparam", "-genkey", "-name", "prime256v1", "-out", keyPath]);
    execFileSync("openssl", [
      "req", "-new", "-key", keyPath, "-out", csrPath, "-subj", `/CN=qrdb-node-${i + 1}`,
    ]);
    execFileSync("openssl", [
      "x509", "-req", "-in", csrPath, "-CA", caPath, "-CAkey", caKey,
      "-CAcreateserial", "-days", "1", "-out", certPath, "-extfile", extPath,
    ]);
    nodes.push({ certPath, keyPath });
  }
  return { caPath, nodes };
}

function writeClusterConfig(path: string): void {
  // seal disabled: this fixture exercises cluster topology UI, not barriers.
  const lines = [
    "codec:",
    '  storage_codec: "json"',
    '  wire_codec: "json"',
    "seal:",
    "  enabled: false",
    // auth.enabled requires seal.enabled, so a seal-off cluster runs auth-off.
    "auth:",
    "  enabled: false",
  ];
  writeFileSync(path, lines.join("\n") + "\n", { mode: 0o600 });
}

async function httpsGetStatus(url: string, timeoutMs = 1000): Promise<{ code: number; body: string } | null> {
  const https = await import("https");
  return new Promise((resolve) => {
    const req = https.default.get(url, { rejectUnauthorized: false }, (res) => {
      const chunks: Buffer[] = [];
      res.on("data", (c: Buffer) => chunks.push(c));
      res.on("end", () => resolve({ code: res.statusCode ?? 0, body: Buffer.concat(chunks).toString() }));
    });
    req.on("error", () => resolve(null));
    req.setTimeout(timeoutMs, () => {
      req.destroy();
      resolve(null);
    });
  });
}

/**
 * Polls node 1's /admin/topology until the system Raft group reports the full
 * member set (membership converged + a leader elected). Returns the observed
 * distinct node count.
 */
async function waitForClusterTopology(adminUrl: string, want: number, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let last = "no response";
  while (Date.now() < deadline) {
    const res = await httpsGetStatus(`${adminUrl}/admin/topology`);
    if (res !== null && res.code === 200) {
      try {
        const topo = JSON.parse(res.body) as { shards?: { nodes?: Record<string, string> }[] };
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
  throw new Error(`cluster topology did not reach ${want} nodes within ${timeoutMs}ms (last: ${last})`);
}

/** Boots a 3-node cluster and blocks until topology reports all members. */
export async function startCluster(nodeCount = 3): Promise<DomCluster> {
  const certDir = mkdtempSync(join(tmpdir(), "qrdb-dom-cluster-certs-"));
  const { caPath, nodes: nodeCerts } = generateClusterCerts(certDir, nodeCount);

  const peersFlag = NODE_PORTS.slice(0, nodeCount)
    .map((p, i) => `${i + 1}=127.0.0.1:${p.raft}`)
    .join(",");

  // quicraft admits a member only with its transport identity: every node is
  // told the identities (`qrdbd node-identity`) of the other replicas.
  const identities = nodeCerts.map((c, i) => {
    const out = execFileSync(qrdbdBin(), ["node-identity", "--cert", c.certPath]).toString().trim();
    const id = out.split(/\s+/).find((t) => t.startsWith("spki:")) ?? out;
    return `${i + 1}=${id}`;
  });

  const procs: ChildProcess[] = [];
  const dataDirs: string[] = [];
  const stderrBufs: Buffer[][] = [];

  for (let i = 0; i < nodeCount; i++) {
    const ports = NODE_PORTS[i]!;
    const dataDir = mkdtempSync(join(tmpdir(), `qrdb-dom-cluster-n${i + 1}-`));
    dataDirs.push(dataDir);
    const configPath = join(dataDir, "config.yaml");
    writeClusterConfig(configPath);

    const args = [
      "start",
      "--config", configPath,
      "--data-dir", dataDir,
      "--listen", `0.0.0.0:${ports.raft}`,
      "--raft-address", `127.0.0.1:${ports.raft}`,
      "--admin-port", String(ports.admin),
      "--grpc-port", String(ports.grpc),
      "--auth-enabled=false",
      "--tls-client-auth=none",
      "--discovery", "static",
      "--peers", peersFlag,
      ...(nodeCount > 1
        ? ["--member-identities", identities.filter((_, j) => j !== i).join(",")]
        : []),
      "--shard-id", "1",
      "--replica-id", String(i + 1),
      "--cluster-trust-domain", CLUSTER_TRUST_DOMAIN,
      "--tls-ca-file", caPath,
      "--tls-cert-file", nodeCerts[i]!.certPath,
      "--tls-key-file", nodeCerts[i]!.keyPath,
      "--log-level", "warn",
      "--enable-grpc=false",
      "--enable-unix=false",
      "--enable-quic=false",
      "--enable-mcp=false",
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
  try {
    await waitForClusterTopology(adminUrl, nodeCount);
  } catch (err) {
    for (const p of procs) p.kill("SIGTERM");
    const logs = stderrBufs.map((b, i) => `--- node${i + 1} stderr ---\n${Buffer.concat(b).toString()}`).join("\n");
    throw new Error(`${err}\n${logs}`);
  }

  return { adminUrl, nodeCount, procs, dataDirs, certDir };
}

/** Stops all cluster nodes and removes their data + cert directories. */
export async function stopCluster(cluster: DomCluster | null): Promise<void> {
  if (cluster === null) return;
  for (const p of cluster.procs) p.kill("SIGTERM");
  await new Promise((r) => setTimeout(r, 600));
  for (const dir of [...cluster.dataDirs, cluster.certDir]) {
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch {
      // best effort
    }
  }
}
