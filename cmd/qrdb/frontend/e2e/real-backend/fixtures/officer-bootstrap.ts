// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * officer-bootstrap.ts
 *
 * Shared helpers for the Security Officer (SO) enrollment and share-claim
 * flows described in §6 of docs/seal/so-share-distribution-design.md.
 *
 * The helpers cover:
 *   - EC P-256 key-pair generation and CSR production (via openssl CLI)
 *   - Officer registration against the system-barrier REST API
 *   - Share claim (begin → sign nonce → complete) using a per-officer
 *     mTLS request context
 *   - A convenience bootstrap function that registers N officers,
 *     initialises the Shamir barrier, and claims all shares
 *
 * Signature scheme (mirrors verifyNonceSignature in pkg/ceremony/crypto.go):
 *   ECDSA P-256: sign SHA-256(rawNonceBytes) → ASN.1 DER bytes → base64-StdEncoding
 *   In Node: crypto.createSign("SHA256").update(<rawNonceBytes>).sign(<privateKeyPem>)
 *   produces exactly this encoding.
 *
 * share_bytes decoding:
 *   The REST handler base64-StdEncoding-encodes JSON(shamir.Share{}).
 *   Decode: base64 → JSON → .value  (the combinable share value string).
 */

import type { APIRequest, APIRequestContext } from "playwright-core";
import { execFileSync } from "child_process";
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { createSign, generateKeyPairSync } from "crypto";
import { waitForHealthExported, waitForSealedWindow } from "./daemon-helpers.ts";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** A generated EC P-256 key pair with a signed CSR. */
export interface OfficerKey {
  username: string;
  privateKeyPem: string;
  publicKeyPem: string;
  csrPem: string;
}

/** The result of registering an officer with the barrier API. */
export interface RegisteredOfficer {
  username: string;
  privateKeyPem: string;
  publicKeyPem: string;
  certPem: string;
  shareIndex: number;
}

/** A claimed Shamir share. */
export interface ClaimedShare {
  username: string;
  shareIndex: number;
  /** The combinable share value extracted from the claim response. */
  shareValue: string;
}

/** The complete bootstrap result returned by bootstrapShamirBarrier. */
export interface BootstrapResult {
  /** Claimed share values in registration order (officer[0].shareValue, ...). */
  shareValues: string[];
  /** Registered officer information in registration order. */
  officers: RegisteredOfficer[];
}

// ---------------------------------------------------------------------------
// Key and CSR generation
// ---------------------------------------------------------------------------

/**
 * generateOfficerKeyAndCSR generates an EC P-256 key pair and produces a
 * PEM-encoded CSR for the given username via the system openssl CLI.
 *
 * openssl is used for CSR generation because Node's `crypto` module does not
 * expose a CSR builder. The key is generated natively via Node's
 * `generateKeyPairSync` so it stays in-process and only the private key PEM
 * is written to a temp file for openssl req.
 */
export function generateOfficerKeyAndCSR(username: string): OfficerKey {
  // Generate EC P-256 key pair in process.
  const { privateKey, publicKey } = generateKeyPairSync("ec", {
    namedCurve: "P-256",
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });

  const privateKeyPem = privateKey as string;
  const publicKeyPem = publicKey as string;

  // Write private key to a temp file for openssl req.
  const tmpDir = mkdtempSync(join(tmpdir(), `qrdb-officer-csr-`));
  const keyPath = join(tmpDir, "key.pem");
  const csrPath = join(tmpDir, "csr.pem");

  try {
    writeFileSync(keyPath, privateKeyPem, { mode: 0o600 });

    // Generate CSR. openssl req -new -key <key> -out <csr> -subj "/CN=<username>"
    execFileSync(
      "openssl",
      [
        "req",
        "-new",
        "-key",
        keyPath,
        "-out",
        csrPath,
        "-subj",
        `/CN=${username}`,
      ],
      { stdio: ["ignore", "ignore", "ignore"] },
    );

    // Node's built-in fs.readFileSync for reading the generated CSR.
    const csrPem = readFileSync(csrPath, "utf8");
    return { username, privateKeyPem, publicKeyPem, csrPem };
  } finally {
    try {
      rmSync(tmpDir, { recursive: true, force: true });
    } catch {
      // best effort cleanup
    }
  }
}

// ---------------------------------------------------------------------------
// Officer registration
// ---------------------------------------------------------------------------

/**
 * registerOfficers submits each officer CSR to POST /v1/barrier/officers and
 * returns the registered officers with their signed certificates.
 *
 * Uses the base `request` fixture context (no client cert) because
 * registration is an admin operation, not a per-officer mTLS operation.
 */
export async function registerOfficers(
  request: APIRequestContext,
  adminUrl: string,
  officers: OfficerKey[],
): Promise<RegisteredOfficer[]> {
  const registered: RegisteredOfficer[] = [];

  for (const officer of officers) {
    const resp = await request.post(`${adminUrl}/v1/barrier/officers`, {
      data: {
        username: officer.username,
        csr_pem: officer.csrPem,
      },
      headers: { "Content-Type": "application/json" },
    });

    if (!resp.ok()) {
      const body = await resp.text();
      throw new Error(
        `Failed to register officer ${officer.username}: HTTP ${resp.status()} — ${body}`,
      );
    }

    const body = (await resp.json()) as {
      cert_pem: string;
      share_index: number;
    };

    registered.push({
      username: officer.username,
      privateKeyPem: officer.privateKeyPem,
      publicKeyPem: officer.publicKeyPem,
      certPem: body.cert_pem,
      shareIndex: body.share_index,
    });
  }

  return registered;
}

// ---------------------------------------------------------------------------
// Share claim
// ---------------------------------------------------------------------------

/**
 * claimShare performs the two-step share claim for a single officer:
 *
 *  1. POST /v1/barrier/officers/{username}/claim/begin (mTLS with officer cert)
 *     → {nonce_hex, expires_at}
 *  2. Sign nonce: createSign("SHA256").update(Buffer.from(nonceHex, "hex")).sign(privateKeyPem)
 *     → ASN.1-DER ECDSA signature → base64-StdEncoding
 *  3. POST /v1/barrier/officers/{username}/claim/complete {nonce_hex, signature}
 *     → {share_index, share_bytes}
 *  4. Decode share_bytes: base64 → JSON → .value
 *
 * Accepts `playwright.request` (APIRequest) to create a per-officer request
 * context with client certificate material for the officer's mTLS session.
 * The context is disposed after the claim completes.
 */
export async function claimShare(
  playwrightRequest: APIRequest,
  adminUrl: string,
  officer: RegisteredOfficer,
): Promise<ClaimedShare> {
  const ctx: APIRequestContext = await playwrightRequest.newContext({
    ignoreHTTPSErrors: true,
    clientCertificates: [
      {
        origin: adminUrl,
        cert: Buffer.from(officer.certPem),
        key: Buffer.from(officer.privateKeyPem),
      },
    ],
  });

  try {
    // Step 1: begin claim.
    const beginResp = await ctx.post(
      `${adminUrl}/v1/barrier/officers/${officer.username}/claim/begin`,
      { headers: { "Content-Type": "application/json" } },
    );

    if (!beginResp.ok()) {
      const body = await beginResp.text();
      throw new Error(
        `claim/begin failed for ${officer.username}: HTTP ${beginResp.status()} — ${body}`,
      );
    }

    const beginBody = (await beginResp.json()) as {
      nonce_hex: string;
      expires_at: string;
    };

    const nonceHex = beginBody.nonce_hex;

    // Step 2: sign the raw nonce bytes.
    // verifyNonceSignature (pkg/ceremony/crypto.go) for ECDSA P-256 expects:
    //   digest = SHA-256(rawNonceBytes)
    //   signature = ASN.1 DER encoding of ECDSA(digest)
    // Node's crypto.createSign("SHA256").update(rawBytes).sign(key) produces
    // exactly SHA-256 + ASN.1-DER for EC keys.
    const rawNonce = Buffer.from(nonceHex, "hex");
    const sign = createSign("SHA256");
    sign.update(rawNonce);
    const signatureDer = sign.sign(officer.privateKeyPem);
    const signatureB64 = signatureDer.toString("base64");

    // Step 3: complete claim.
    const completeResp = await ctx.post(
      `${adminUrl}/v1/barrier/officers/${officer.username}/claim/complete`,
      {
        data: { nonce_hex: nonceHex, signature: signatureB64 },
        headers: { "Content-Type": "application/json" },
      },
    );

    if (!completeResp.ok()) {
      const body = await completeResp.text();
      throw new Error(
        `claim/complete failed for ${officer.username}: HTTP ${completeResp.status()} — ${body}`,
      );
    }

    const completeBody = (await completeResp.json()) as {
      share_index: number;
      share_bytes: string;
    };

    // Step 4: decode share_bytes → JSON → .value.
    // share_bytes = base64-StdEncoding of JSON-marshaled shamir.Share.
    // The combinable share value is the .value string field in that JSON.
    const shareJson = Buffer.from(completeBody.share_bytes, "base64").toString(
      "utf8",
    );
    const share = JSON.parse(shareJson) as { value: string };

    return {
      username: officer.username,
      shareIndex: completeBody.share_index,
      shareValue: share.value,
    };
  } finally {
    await ctx.dispose();
  }
}

// ---------------------------------------------------------------------------
// Recovery officer claim (mirrors claimShare but uses recovery routes)
// ---------------------------------------------------------------------------

/**
 * claimRecoveryShare performs the two-step recovery share claim for a single
 * officer using the /v1/barrier/recovery/officers/{username}/claim/* routes.
 */
export async function claimRecoveryShare(
  playwrightRequest: APIRequest,
  adminUrl: string,
  officer: RegisteredOfficer,
): Promise<ClaimedShare> {
  const ctx: APIRequestContext = await playwrightRequest.newContext({
    ignoreHTTPSErrors: true,
    clientCertificates: [
      {
        origin: adminUrl,
        cert: Buffer.from(officer.certPem),
        key: Buffer.from(officer.privateKeyPem),
      },
    ],
  });

  try {
    const beginResp = await ctx.post(
      `${adminUrl}/v1/barrier/recovery/officers/${officer.username}/claim/begin`,
      { headers: { "Content-Type": "application/json" } },
    );

    if (!beginResp.ok()) {
      const body = await beginResp.text();
      throw new Error(
        `recovery claim/begin failed for ${officer.username}: HTTP ${beginResp.status()} — ${body}`,
      );
    }

    const beginBody = (await beginResp.json()) as {
      nonce_hex: string;
      expires_at: string;
    };

    const nonceHex = beginBody.nonce_hex;
    const rawNonce = Buffer.from(nonceHex, "hex");
    const sign = createSign("SHA256");
    sign.update(rawNonce);
    const signatureDer = sign.sign(officer.privateKeyPem);
    const signatureB64 = signatureDer.toString("base64");

    const completeResp = await ctx.post(
      `${adminUrl}/v1/barrier/recovery/officers/${officer.username}/claim/complete`,
      {
        data: { nonce_hex: nonceHex, signature: signatureB64 },
        headers: { "Content-Type": "application/json" },
      },
    );

    if (!completeResp.ok()) {
      const body = await completeResp.text();
      throw new Error(
        `recovery claim/complete failed for ${officer.username}: HTTP ${completeResp.status()} — ${body}`,
      );
    }

    const completeBody = (await completeResp.json()) as {
      share_index: number;
      share_bytes: string;
    };

    const shareJson = Buffer.from(completeBody.share_bytes, "base64").toString(
      "utf8",
    );
    const share = JSON.parse(shareJson) as { value: string };

    return {
      username: officer.username,
      shareIndex: completeBody.share_index,
      shareValue: share.value,
    };
  } finally {
    await ctx.dispose();
  }
}

// ---------------------------------------------------------------------------
// Full bootstrap helper
// ---------------------------------------------------------------------------

/**
 * bootstrapShamirBarrier performs the complete register-N → initialize →
 * claim-all flow for the system Shamir barrier.
 *
 * Steps:
 *  1. Generate officer keys and CSRs (one per entry in officerUsernames).
 *  2. Register all officers via POST /v1/barrier/officers.
 *  3. POST /v1/barrier/shamir/initialize with the given threshold and total.
 *     (total must equal officerUsernames.length; init HARD-FAILS otherwise.)
 *  4. Each officer claims their share via the mTLS claim handshake.
 *
 * Returns { shareValues, officers } where shareValues[i] is the combinable
 * share value for officers[i].
 *
 * @param request - The test's base APIRequestContext (no client cert).
 * @param playwrightRequest - playwright.request (APIRequest) for creating
 *   per-officer mTLS contexts during the claim step.
 */
export async function bootstrapShamirBarrier(
  request: APIRequestContext,
  playwrightRequest: APIRequest,
  adminUrl: string,
  opts: {
    officerUsernames: string[];
    threshold: number;
    total: number;
    secret: string;
  },
): Promise<BootstrapResult> {
  const { officerUsernames, threshold, total, secret } = opts;

  if (officerUsernames.length !== total) {
    throw new Error(
      `bootstrapShamirBarrier: officerUsernames.length (${officerUsernames.length}) must equal total (${total})`,
    );
  }

  // 1. Generate keys + CSRs.
  const keys = officerUsernames.map((u) => generateOfficerKeyAndCSR(u));

  // 2. Register officers.
  const officers = await registerOfficers(request, adminUrl, keys);

  // 3. Initialize the barrier (share-free; shares go to officer enclaves).
  const initResp = await request.post(
    `${adminUrl}/v1/barrier/shamir/initialize`,
    {
      data: { secret, threshold, total_shares: total },
      headers: { "Content-Type": "application/json" },
    },
  );

  if (!initResp.ok()) {
    const body = await initResp.text();
    throw new Error(
      `shamir/initialize failed: HTTP ${initResp.status()} — ${body}`,
    );
  }

  // shamir/initialize auto-unseals the barrier, which swaps the daemon's
  // sealed-window listener for the full server: the sealed-window server
  // drains while the host + first-boot phases run (several seconds), and
  // requests issued during the swap fail with socket-hang-up/ECONNREFUSED.
  // Wait for /health 200 (full server up) before claiming any shares.
  await waitForHealthExported(adminUrl, 60_000);

  // 4. Claim all shares.
  const claims: ClaimedShare[] = [];
  for (const officer of officers) {
    const claimed = await claimShare(playwrightRequest, adminUrl, officer);
    claims.push(claimed);
  }

  // Align shareValues to the officers slice by shareIndex.
  const shareValues = officers.map((o) => {
    const claim = claims.find((c) => c.shareIndex === o.shareIndex);
    if (!claim) {
      throw new Error(
        `No claim found for officer ${o.username} (shareIndex=${o.shareIndex})`,
      );
    }
    return claim.shareValue;
  });

  return { shareValues, officers };
}

// ---------------------------------------------------------------------------
// Seal-vote quorum helper
// ---------------------------------------------------------------------------

/**
 * sealBarrierViaQuorum seals an UNSEALED shamir barrier by submitting one
 * POST /v1/barrier/shamir/seal-vote per provided share value.
 *
 * Shamir barriers cannot be sealed via POST /v1/barrier/seal (the server
 * rejects it with 422 seal.ErrShamirBarrierRequiresQuorum); a quorum of
 * threshold seal votes is required. The final vote triggers the seal, after
 * which the daemon transitions back to the sealed window — this helper polls
 * /v1/barrier/status until it reports sealed:true so callers can immediately
 * submit unseal shares.
 *
 * Pass exactly `threshold` share values.
 */
export async function sealBarrierViaQuorum(
  request: APIRequestContext,
  adminUrl: string,
  shareValues: string[],
): Promise<void> {
  for (const share of shareValues) {
    const resp = await request.post(
      `${adminUrl}/v1/barrier/shamir/seal-vote`,
      {
        data: { share },
        headers: { "Content-Type": "application/json" },
      },
    );
    if (!resp.ok()) {
      const body = await resp.text();
      throw new Error(`seal-vote failed: HTTP ${resp.status()} — ${body}`);
    }
  }

  await waitForSealedWindow(adminUrl, 60_000);
}
