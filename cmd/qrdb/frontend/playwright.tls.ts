// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Ephemeral TLS for the Vite server the Playwright tiers browse.
 *
 * Web mode is served by qrdbd over TLS only, and every secret-bearing SDK call
 * refuses a non-https base URL. A tier that served the bundle over http would
 * test an origin production never has, and its secret-bearing specs would fail
 * at the SDK before any request is sent. Each run therefore serves over https
 * with a self-signed certificate generated here, in memory, with node:crypto
 * alone: no cert or key file is written or committed, and no dependency is
 * added. The browser accepts the certificate only through the tier's own
 * `ignoreHTTPSErrors`.
 *
 * The PEMs travel to the Vite process in TLS_CERT_ENV and TLS_KEY_ENV, which
 * vite.config.ts reads. The certificate is a v3 X.509 certificate (RFC 5280)
 * over an ECDSA P-256 key, signed ecdsa-with-SHA256 (RFC 5758 §3.2), naming
 * localhost and 127.0.0.1 in subjectAltName.
 */

import { generateKeyPairSync, randomBytes, sign, X509Certificate } from "node:crypto";

/** Names the env var carrying the Vite server's PEM certificate. */
export const TLS_CERT_ENV = "QRDB_VITE_TLS_CERT";

/** Names the env var carrying the Vite server's PEM PKCS#8 private key. */
export const TLS_KEY_ENV = "QRDB_VITE_TLS_KEY";

/**
 * Names the directory the Vite API proxy reads a client certificate from
 * (client.crt and client.key, PEM) on every connection to the backend, so a
 * DOM spec can make the UI an authenticated principal
 * (e2e/dom/fixtures/dom-daemon.ts tenantAdmin). With no files there the proxy
 * presents none.
 */
export const PROXY_CLIENT_TLS_DIR_ENV = "QRDB_PROXY_CLIENT_TLS_DIR";

/** How long the certificate is valid for; a run is far shorter. */
const VALIDITY_MS = 24 * 60 * 60 * 1000;

/** Backdates notBefore so clock skew between processes cannot reject it. */
const BACKDATE_MS = 60 * 60 * 1000;

const OID_ECDSA_SHA256 = "1.2.840.10045.4.3.2";
const OID_COMMON_NAME = "2.5.4.3";
const OID_SUBJECT_ALT_NAME = "2.5.29.17";
const OID_BASIC_CONSTRAINTS = "2.5.29.19";

/** derLength encodes a DER definite length (X.690 §8.1.3). */
function derLength(n: number): Buffer {
  if (n < 0x80) {
    return Buffer.from([n]);
  }
  const bytes: number[] = [];
  for (let v = n; v > 0; v = Math.floor(v / 256)) {
    bytes.unshift(v & 0xff);
  }
  return Buffer.from([0x80 | bytes.length, ...bytes]);
}

/** tlv encodes one DER element with the given identifier octet. */
function tlv(tag: number, ...content: Buffer[]): Buffer {
  const body = Buffer.concat(content);
  return Buffer.concat([Buffer.from([tag]), derLength(body.length), body]);
}

const seq = (...c: Buffer[]): Buffer => tlv(0x30, ...c);
const set = (...c: Buffer[]): Buffer => tlv(0x31, ...c);
const explicit = (n: number, ...c: Buffer[]): Buffer => tlv(0xa0 + n, ...c);

/** oid encodes an OBJECT IDENTIFIER (X.690 §8.19). */
function oid(dotted: string): Buffer {
  const arcs = dotted.split(".").map(Number);
  const out: number[] = [arcs[0] * 40 + arcs[1]];
  for (const arc of arcs.slice(2)) {
    const groups: number[] = [arc & 0x7f];
    for (let v = Math.floor(arc / 128); v > 0; v = Math.floor(v / 128)) {
      groups.unshift(0x80 | (v & 0x7f));
    }
    out.push(...groups);
  }
  return tlv(0x06, Buffer.from(out));
}

/** utcTime encodes a UTCTime as YYMMDDHHMMSSZ (RFC 5280 §4.1.2.5.1). */
function utcTime(d: Date): Buffer {
  const two = (n: number): string => String(n).padStart(2, "0");
  const s =
    two(d.getUTCFullYear() % 100) + two(d.getUTCMonth() + 1) + two(d.getUTCDate()) +
    two(d.getUTCHours()) + two(d.getUTCMinutes()) + two(d.getUTCSeconds()) + "Z";
  return tlv(0x17, Buffer.from(s, "ascii"));
}

/** name encodes a Name holding the one commonName cn. */
function name(cn: string): Buffer {
  return seq(set(seq(oid(OID_COMMON_NAME), tlv(0x0c, Buffer.from(cn, "utf8")))));
}

/** serialNumber returns a positive 16-byte serial (RFC 5280 §4.1.2.2). */
function serialNumber(): Buffer {
  const serial = randomBytes(16);
  // Clear the sign bit and set the next one, so the INTEGER is positive and
  // its first octet is non-zero: minimal DER needs no leading pad.
  serial[0] = (serial[0] & 0x7f) | 0x40;
  return tlv(0x02, serial);
}

/** toPEM wraps DER bytes in a PEM block of the given label (RFC 7468). */
function toPEM(label: string, der: Buffer): string {
  const lines = der.toString("base64").match(/.{1,64}/g) ?? [];
  return `-----BEGIN ${label}-----\n${lines.join("\n")}\n-----END ${label}-----\n`;
}

/** EphemeralTLS is a PEM certificate and its PEM PKCS#8 private key. */
export interface EphemeralTLS {
  cert: string;
  key: string;
}

/**
 * selfSignedCertificate generates a fresh key pair and a self-signed
 * certificate for localhost and 127.0.0.1, valid from an hour ago for a day.
 * It verifies the certificate's signature before returning it, so a malformed
 * encoding fails here rather than as a TLS handshake error in the browser.
 */
export function selfSignedCertificate(now: Date = new Date()): EphemeralTLS {
  const { publicKey, privateKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const sigAlg = seq(oid(OID_ECDSA_SHA256));
  const subject = name("qrdb playwright");
  const altNames = seq(
    tlv(0x82, Buffer.from("localhost", "ascii")), // dNSName [2]
    tlv(0x87, Buffer.from([127, 0, 0, 1])), // iPAddress [7]
  );
  const extensions = seq(
    seq(oid(OID_BASIC_CONSTRAINTS), tlv(0x01, Buffer.from([0xff])), tlv(0x04, seq())),
    seq(oid(OID_SUBJECT_ALT_NAME), tlv(0x04, altNames)),
  );
  const tbs = seq(
    explicit(0, tlv(0x02, Buffer.from([2]))), // v3
    serialNumber(),
    sigAlg,
    subject,
    seq(
      utcTime(new Date(now.getTime() - BACKDATE_MS)),
      utcTime(new Date(now.getTime() + VALIDITY_MS)),
    ),
    subject,
    publicKey.export({ type: "spki", format: "der" }),
    explicit(3, extensions),
  );
  // For an EC key node:crypto emits the DER Ecdsa-Sig-Value X.509 carries.
  const signature = sign("sha256", tbs, privateKey);
  const der = seq(tbs, sigAlg, tlv(0x03, Buffer.from([0]), signature));
  const cert = toPEM("CERTIFICATE", der);
  if (!new X509Certificate(cert).verify(publicKey)) {
    throw new Error("playwright.tls: generated certificate does not verify");
  }
  return { cert, key: privateKey.export({ type: "pkcs8", format: "pem" }).toString() };
}

/**
 * resolveTLS returns the run's certificate and key. A pair already in
 * TLS_CERT_ENV and TLS_KEY_ENV wins; otherwise a fresh pair is generated and
 * written back, so the worker processes Playwright starts, which load the
 * config again and inherit the environment, see the pair the runner's web
 * server uses rather than generating their own.
 */
export function resolveTLS(): EphemeralTLS {
  const cert = process.env[TLS_CERT_ENV];
  const key = process.env[TLS_KEY_ENV];
  if (cert && key) {
    return { cert, key };
  }
  if (cert || key) {
    throw new Error(`${TLS_CERT_ENV} and ${TLS_KEY_ENV} must be set together`);
  }
  const tls = selfSignedCertificate();
  process.env[TLS_CERT_ENV] = tls.cert;
  process.env[TLS_KEY_ENV] = tls.key;
  return tls;
}
