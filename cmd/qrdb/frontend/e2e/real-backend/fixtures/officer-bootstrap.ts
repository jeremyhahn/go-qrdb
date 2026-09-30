// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * officer-bootstrap.ts
 *
 * Shared helpers for share-holder flows against a real qrdbd.
 *
 * Holders (Security Officers and key custodians) are ENROLLED over REST, the
 * way the web UI enrols them. Everything that needs a holder's private key —
 * claiming the sealed envelope and submitting a share to a quorum round — is
 * done with the qrdb CLI, the canonical holder tool, exactly as a holder
 * does it. The envelope is HPKE-sealed and a submission carries a factor
 * proof, so re-implementing either here would mean doing holder cryptography
 * in test code; the CLI keeps it in Go.
 *
 * Binaries: the daemon is QRDBD_BIN (default <repo>/bin/qrdbd); the client
 * CLI is QRDB_BIN (default <repo>/bin/qrdb), a headless build of ./cmd/qrdb.
 */

import type { APIRequest, APIRequestContext } from "playwright-core";
import { execFileSync, spawnSync } from "child_process";
import { mkdtempSync, writeFileSync, readFileSync } from "fs";
import { tmpdir } from "os";
import { join, dirname } from "path";
import { fileURLToPath } from "url";
import { generateKeyPairSync } from "crypto";
import { waitForHealthExported } from "./daemon-helpers.ts";

const __dirname = dirname(fileURLToPath(import.meta.url));

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** A holder's role. */
export type HolderRole = "so" | "custodian";

/** Where a holder belongs: a system-barrier domain or a tenant barrier. */
export interface HolderScope {
  domain?: "unseal" | "recovery";
  tenantId?: string;
}

/** A holder's key material and files on disk. */
export interface Holder {
  username: string;
  role: HolderRole;
  /** PKCS#8 PEM private key of the holder's certificate. */
  keyPath: string;
  csrPem: string;
  /** The CA-issued certificate, written once the holder is enrolled. */
  certPath: string;
  /** Where the claimed envelope is written. */
  envelopePath: string;
}

/** The daemon a holder talks to. */
export interface HolderDaemon {
  adminUrl: string;
  dataDir: string;
}

/** A quorum outcome, as the CLI prints it with --output json. */
export interface QuorumOutcome {
  domain: string;
  op: string;
  required: number;
  submitted: number;
  complete: boolean;
  root_token?: string;
  threshold?: number;
  total_shares?: number;
  share_count?: number;
}

/** The result of a CLI run. */
export interface CliResult {
  status: number;
  stdout: string;
  stderr: string;
}

// ---------------------------------------------------------------------------
// Keys and CSRs
// ---------------------------------------------------------------------------

/**
 * newHolder generates an EC P-256 key and a CSR for username in a private
 * temporary directory. openssl builds the CSR because Node's crypto has no
 * CSR builder.
 */
export function newHolder(username: string, role: HolderRole = "so"): Holder {
  const { privateKey } = generateKeyPairSync("ec", {
    namedCurve: "P-256",
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
    publicKeyEncoding: { type: "spki", format: "pem" },
  });
  const dir = mkdtempSync(join(tmpdir(), `qrdb-holder-${username}-`));
  const keyPath = join(dir, "key.pem");
  const csrPath = join(dir, "csr.pem");
  writeFileSync(keyPath, privateKey as string, { mode: 0o600 });
  execFileSync("openssl", ["req", "-new", "-key", keyPath, "-out", csrPath, "-subj", `/CN=${username}`], {
    stdio: ["ignore", "ignore", "ignore"],
  });
  return {
    username,
    role,
    keyPath,
    csrPem: readFileSync(csrPath, "utf8"),
    certPath: join(dir, "cert.pem"),
    envelopePath: join(dir, "envelope.json"),
  };
}

// ---------------------------------------------------------------------------
// Enrolment (REST, as the web UI does it)
// ---------------------------------------------------------------------------

/** The REST route that enrols a holder in scope. */
export function enrolRoute(adminUrl: string, scope: HolderScope = {}): string {
  if (scope.tenantId) return `${adminUrl}/v1/tenants/${encodeURIComponent(scope.tenantId)}/barrier/officers`;
  if (scope.domain === "recovery") return `${adminUrl}/v1/barrier/recovery/officers`;
  return `${adminUrl}/v1/barrier/officers`;
}

/**
 * enrolHolders enrols each holder in scope and writes its issued certificate
 * to certPath. It fails the test on any non-2xx answer.
 */
export async function enrolHolders(
  request: APIRequestContext,
  adminUrl: string,
  holders: Holder[],
  scope: HolderScope = {},
): Promise<void> {
  for (const h of holders) {
    const resp = await request.post(enrolRoute(adminUrl, scope), {
      data: { username: h.username, role: h.role, csr_pem: h.csrPem },
    });
    if (!resp.ok()) {
      throw new Error(`enrol ${h.username}: ${resp.status()} ${await resp.text()}`);
    }
    const body = (await resp.json()) as { cert_pem: string };
    writeFileSync(h.certPath, body.cert_pem);
  }
}

// ---------------------------------------------------------------------------
// The qrdb CLI
// ---------------------------------------------------------------------------

function qrdbBin(): string {
  return process.env["QRDB_BIN"] ?? join(__dirname, "..", "..", "..", "..", "..", "..", "bin", "qrdb");
}

/**
 * qrdbAs runs the qrdb CLI against the daemon over REST, presenting the
 * holder's certificate on the connection and trusting the daemon's CA.
 */
export function qrdbAs(daemon: HolderDaemon, holder: Holder, args: string[]): CliResult {
  const res = spawnSync(
    qrdbBin(),
    [
      "--server", daemon.adminUrl,
      "--ca-file", join(daemon.dataDir, "ca.pem"),
      "--cert-file", holder.certPath,
      "--key-file", holder.keyPath,
      "--timeout", "20s",
      "--output", "json",
      ...args,
    ],
    { encoding: "utf8" },
  );
  return { status: res.status ?? -1, stdout: res.stdout ?? "", stderr: res.stderr ?? "" };
}

/** The CLI verbs and scope flags for a holder scope. */
function scoped(scope: HolderScope, verb: "officer" | "quorum"): string[] {
  if (scope.tenantId) {
    return ["tenant", verb === "officer" ? "barrier-officer" : "barrier-quorum", "--tenant-id", scope.tenantId];
  }
  return ["barrier", verb, "--domain", scope.domain ?? "unseal"];
}

/** claimEnvelope claims the holder's envelope with the CLI; returns the run. */
export function claimEnvelope(daemon: HolderDaemon, holder: Holder, scope: HolderScope = {}): CliResult {
  const [group, verb, ...flags] = scoped(scope, "officer");
  return qrdbAs(daemon, holder, [
    group!, verb!, "claim", ...flags,
    "--username", holder.username,
    "--key", holder.keyPath,
    "--out", holder.envelopePath,
  ]);
}

/** mustClaim claims the envelope and fails the test if the claim fails. */
export function mustClaim(daemon: HolderDaemon, holder: Holder, scope: HolderScope = {}): void {
  const res = claimEnvelope(daemon, holder, scope);
  if (res.status !== 0) {
    throw new Error(`claim ${holder.username}: exit ${res.status}: ${res.stderr}${res.stdout}`);
  }
}

/**
 * submitShare opens the holder's envelope and submits its share to the open
 * round for op, answering the mtls factor with the certificate key.
 */
export function submitShare(
  daemon: HolderDaemon,
  holder: Holder,
  op: "unseal" | "seal" | "root-token" | "rekey" | "recover",
  scope: HolderScope = {},
): CliResult {
  const [group, verb, ...flags] = scoped(scope, "quorum");
  return qrdbAs(daemon, holder, [
    group!, verb!, "submit", ...flags,
    "--op", op,
    "--username", holder.username,
    "--envelope", holder.envelopePath,
    "--key", holder.keyPath,
  ]);
}

/** mustSubmit submits and returns the parsed outcome, failing the test on error. */
export function mustSubmit(
  daemon: HolderDaemon,
  holder: Holder,
  op: "unseal" | "seal" | "root-token" | "rekey" | "recover",
  scope: HolderScope = {},
): QuorumOutcome {
  const res = submitShare(daemon, holder, op, scope);
  if (res.status !== 0) {
    throw new Error(`submit ${op} by ${holder.username}: exit ${res.status}: ${res.stderr}${res.stdout}`);
  }
  return JSON.parse(res.stdout) as QuorumOutcome;
}

// ---------------------------------------------------------------------------
// Composite flows
// ---------------------------------------------------------------------------

/** The result of bootstrapShamirBarrier. */
export interface ShamirBootstrap {
  holders: Holder[];
  threshold: number;
}

/**
 * bootstrapShamirBarrier enrols n unseal holders, initializes the Shamir
 * barrier with the given threshold, and has every holder claim its envelope.
 * The barrier is unsealed on return. soPin, when given, provisions the SO PIN
 * at initialization; rekey and revocation require it.
 */
export async function bootstrapShamirBarrier(
  request: APIRequestContext,
  daemon: HolderDaemon,
  n: number,
  threshold: number,
  soPin?: string,
): Promise<ShamirBootstrap> {
  const holders = Array.from({ length: n }, (_, i) => newHolder(`holder-${i + 1}`, i === 0 ? "so" : "custodian"));
  await enrolHolders(request, daemon.adminUrl, holders);
  const init = await request.post(`${daemon.adminUrl}/v1/barrier/shamir/initialize`, {
    data: { threshold, total_shares: n, ...(soPin ? { so_pin: soPin } : {}) },
  });
  if (!init.ok()) {
    throw new Error(`shamir/initialize: ${init.status()} ${await init.text()}`);
  }
  // Initialization unseals and swaps the sealed-window listener for the full
  // server; wait out the swap before the holders connect.
  await waitForHealthExported(daemon.adminUrl, 60_000);
  for (const h of holders) mustClaim(daemon, h);
  return { holders, threshold };
}

/**
 * quorum has the first `count` holders submit to op in turn and returns the
 * last outcome.
 */
export function quorum(
  daemon: HolderDaemon,
  holders: Holder[],
  count: number,
  op: "unseal" | "seal" | "root-token" | "rekey" | "recover",
  scope: HolderScope = {},
): QuorumOutcome {
  let last: QuorumOutcome | undefined;
  for (const h of holders.slice(0, count)) last = mustSubmit(daemon, h, op, scope);
  if (last === undefined) throw new Error("quorum: no holder submitted");
  return last;
}

/** waitForSealed polls the barrier status until it reports sealed:want. */
export async function waitForSealed(
  request: APIRequestContext,
  adminUrl: string,
  want: boolean,
  timeoutMs = 30_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const resp = await request.get(`${adminUrl}/v1/barrier/status`);
      if (resp.ok() && ((await resp.json()) as { sealed: boolean }).sealed === want) return;
    } catch {
      // The daemon may be swapping listeners; retry.
    }
    if (Date.now() >= deadline) throw new Error(`barrier did not report sealed=${want} within ${timeoutMs}ms`);
    await new Promise((r) => setTimeout(r, 500));
  }
}

/**
 * holderContext returns a request context presenting cert (and its key) on
 * the TLS connection to adminUrl — for asserting how the REST claim routes
 * answer a given certificate. The caller disposes it.
 */
export async function holderContext(
  apiRequest: APIRequest,
  adminUrl: string,
  cert: Holder,
  key: Holder = cert,
): Promise<APIRequestContext> {
  return apiRequest.newContext({
    ignoreHTTPSErrors: true,
    clientCertificates: [
      { origin: adminUrl, cert: readFileSync(cert.certPath), key: readFileSync(key.keyPath) },
    ],
  });
}
