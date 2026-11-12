/**
 * Local PKCS#12 (RFC 7292) encoding on Web Crypto.
 *
 * No go-qrdb node has a PKCS#12 route: a private key is never sent to a
 * server to be packaged on the caller's behalf (docs/certificate-authority/
 * api.md). The REST client therefore builds the archive in this process,
 * the one that already holds the key, as `qrdb ca export-bundle` and the
 * desktop binding do in Go.
 *
 * Layout (what OpenSSL 3 and go-pkcs12's Modern encoder read):
 * - the certificates in a plain `data` SafeContents, the leaf carrying a
 *   localKeyId of SHA-1(leaf DER);
 * - the key as a pkcs8ShroudedKeyBag under PBES2 (PBKDF2-HMAC-SHA256,
 *   AES-256-CBC), the PBKDF2 password the passphrase's UTF-8 bytes;
 * - an HMAC-SHA256 MacData whose key comes from the RFC 7292 appendix B
 *   KDF over the passphrase as a BMPString.
 *
 * @module
 */

import { CertExportError } from './errors.js';

/** Iteration count for both PBKDF2 and the MAC KDF (OpenSSL's default). */
export const PKCS12_ITERATIONS = 2048;

const OID_DATA = '1.2.840.113549.1.7.1';
const OID_PKCS8_SHROUDED_KEY_BAG = '1.2.840.113549.1.12.10.1.2';
const OID_CERT_BAG = '1.2.840.113549.1.12.10.1.3';
const OID_X509_CERTIFICATE = '1.2.840.113549.1.9.22.1';
const OID_LOCAL_KEY_ID = '1.2.840.113549.1.9.21';
const OID_PBES2 = '1.2.840.113549.1.5.13';
const OID_PBKDF2 = '1.2.840.113549.1.5.12';
const OID_HMAC_SHA256 = '1.2.840.113549.2.9';
const OID_AES256_CBC = '2.16.840.1.101.3.4.1.42';
const OID_SHA256 = '2.16.840.1.101.3.4.2.1';
const OID_RSA_ENCRYPTION = '1.2.840.113549.1.1.1';
const OID_EC_PUBLIC_KEY = '1.2.840.10045.2.1';

// ---------------------------------------------------------------------------
// DER
// ---------------------------------------------------------------------------

function concat(...parts: Uint8Array[]): Uint8Array {
    const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let off = 0;
    for (const p of parts) {
        out.set(p, off);
        off += p.length;
    }
    return out;
}

function tlv(tag: number, ...contents: Uint8Array[]): Uint8Array {
    const body = concat(...contents);
    const n = body.length;
    let len: Uint8Array;
    if (n < 0x80) {
        len = Uint8Array.of(n);
    } else {
        const bytes: number[] = [];
        for (let v = n; v > 0; v = Math.floor(v / 256)) bytes.unshift(v & 0xff);
        len = Uint8Array.of(0x80 | bytes.length, ...bytes);
    }
    return concat(Uint8Array.of(tag), len, body);
}

const seq = (...c: Uint8Array[]): Uint8Array => tlv(0x30, ...c);
const set = (...c: Uint8Array[]): Uint8Array => tlv(0x31, ...c);
const octets = (b: Uint8Array): Uint8Array => tlv(0x04, b);
const explicit0 = (...c: Uint8Array[]): Uint8Array => tlv(0xa0, ...c);
const derNull = (): Uint8Array => Uint8Array.of(0x05, 0x00);

function integer(v: number): Uint8Array {
    const bytes: number[] = [];
    for (let x = v; x > 0; x = Math.floor(x / 256)) bytes.unshift(x & 0xff);
    if (bytes.length === 0 || (bytes[0] & 0x80) !== 0) bytes.unshift(0);
    return tlv(0x02, Uint8Array.from(bytes));
}

function oid(dotted: string): Uint8Array {
    const arcs = dotted.split('.').map(Number);
    const bytes: number[] = [arcs[0] * 40 + arcs[1]];
    for (const arc of arcs.slice(2)) {
        const enc: number[] = [arc & 0x7f];
        for (let v = Math.floor(arc / 128); v > 0; v = Math.floor(v / 128)) enc.unshift(0x80 | (v & 0x7f));
        bytes.push(...enc);
    }
    return tlv(0x06, Uint8Array.from(bytes));
}

/** One decoded DER element: its tag, its contents, and the whole encoding. */
interface Element {
    readonly tag: number;
    readonly body: Uint8Array;
    readonly raw: Uint8Array;
}

/** Reads the DER elements laid end to end in `buf`. */
function children(buf: Uint8Array): Element[] {
    const out: Element[] = [];
    let off = 0;
    while (off < buf.length) {
        const start = off;
        if (off + 2 > buf.length) throw new CertExportError('truncated DER');
        const tag = buf[off++];
        let len = buf[off++];
        if (len & 0x80) {
            const n = len & 0x7f;
            if (n === 0 || n > 4 || off + n > buf.length) throw new CertExportError('bad DER length');
            len = 0;
            for (let i = 0; i < n; i++) len = len * 256 + buf[off++];
        }
        if (off + len > buf.length) throw new CertExportError('truncated DER');
        out.push({ tag, body: buf.subarray(off, off + len), raw: buf.subarray(start, off + len) });
        off += len;
    }
    return out;
}

// ---------------------------------------------------------------------------
// PEM
// ---------------------------------------------------------------------------

interface PemBlock {
    readonly label: string;
    readonly der: Uint8Array;
}

function base64ToBytes(b64: string): Uint8Array {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
}

function bytesToBase64(bytes: Uint8Array): string {
    let bin = '';
    for (let i = 0; i < bytes.length; i += 0x8000) {
        bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    }
    return btoa(bin);
}

function pemBlocks(pem: string): PemBlock[] {
    const re = /-----BEGIN ([A-Z0-9 ]+)-----([\s\S]*?)-----END \1-----/g;
    const out: PemBlock[] = [];
    for (const m of pem.matchAll(re)) {
        const body = m[2].replace(/\s+/g, '');
        if (body.includes(':')) throw new CertExportError(`${m[1]} PEM block has headers (encrypted?)`);
        try {
            out.push({ label: m[1], der: base64ToBytes(body) });
        } catch {
            throw new CertExportError(`${m[1]} PEM block is not valid base64`);
        }
    }
    return out;
}

/** Returns the key as PKCS#8 PrivateKeyInfo DER, wrapping PKCS#1 and SEC1. */
function privateKeyInfo(keyPEM: string): Uint8Array {
    const blocks = pemBlocks(keyPEM).filter((b) => b.label.endsWith('PRIVATE KEY'));
    if (blocks.length !== 1) {
        throw new CertExportError(`key PEM must hold exactly one private key block, found ${blocks.length}`);
    }
    const { label, der } = blocks[0];
    switch (label) {
        case 'PRIVATE KEY':
            children(der); // must parse
            return der;
        case 'RSA PRIVATE KEY':
            return seq(integer(0), seq(oid(OID_RSA_ENCRYPTION), derNull()), octets(der));
        case 'EC PRIVATE KEY': {
            const [ecKey] = children(der);
            const params = ecKey ? children(ecKey.body).find((e) => e.tag === 0xa0) : undefined;
            const curve = params ? children(params.body)[0] : undefined;
            if (!curve || curve.tag !== 0x06) {
                throw new CertExportError('EC PRIVATE KEY names no named curve');
            }
            return seq(integer(0), seq(oid(OID_EC_PUBLIC_KEY), curve.raw), octets(der));
        }
        default:
            throw new CertExportError(`unsupported key PEM block "${label}" (need an unencrypted PRIVATE KEY)`);
    }
}

// ---------------------------------------------------------------------------
// Crypto
// ---------------------------------------------------------------------------

function subtle(): SubtleCrypto {
    const s = globalThis.crypto?.subtle;
    if (!s) throw new CertExportError('Web Crypto (crypto.subtle) is unavailable in this runtime');
    return s;
}

function randomBytes(n: number): Uint8Array {
    return globalThis.crypto.getRandomValues(new Uint8Array(n));
}

async function digest(alg: 'SHA-1' | 'SHA-256', data: Uint8Array): Promise<Uint8Array> {
    return new Uint8Array(await subtle().digest(alg, data as BufferSource));
}

/** The passphrase as a NUL-terminated big-endian BMPString (RFC 7292 B.1). */
function bmpPassword(passphrase: string): Uint8Array {
    const out = new Uint8Array((passphrase.length + 1) * 2);
    for (let i = 0; i < passphrase.length; i++) {
        const c = passphrase.charCodeAt(i);
        out[2 * i] = c >> 8;
        out[2 * i + 1] = c & 0xff;
    }
    return out;
}

/** RFC 7292 appendix B.2 KDF, SHA-256 (u = 32, v = 64), for 32 output bytes. */
export async function pkcs12KDF(
    id: number,
    password: Uint8Array,
    salt: Uint8Array,
    iterations: number,
): Promise<Uint8Array> {
    const v = 64;
    const fill = (src: Uint8Array): Uint8Array => {
        const n = src.length === 0 ? 0 : v * Math.ceil(src.length / v);
        const out = new Uint8Array(n);
        for (let i = 0; i < n; i++) out[i] = src[i % src.length];
        return out;
    };
    let a = concat(new Uint8Array(v).fill(id), fill(salt), fill(password));
    for (let i = 0; i < iterations; i++) a = await digest('SHA-256', a);
    return a;
}

async function hmacSHA256(key: Uint8Array, data: Uint8Array): Promise<Uint8Array> {
    const k = await subtle().importKey('raw', key as BufferSource, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    return new Uint8Array(await subtle().sign('HMAC', k, data as BufferSource));
}

/** PBES2 (PBKDF2-HMAC-SHA256 + AES-256-CBC) EncryptedPrivateKeyInfo. */
async function shroud(pkcs8: Uint8Array, passphrase: string): Promise<Uint8Array> {
    const salt = randomBytes(16);
    const iv = randomBytes(16);
    const base = await subtle().importKey('raw', new TextEncoder().encode(passphrase), 'PBKDF2', false, ['deriveKey']);
    const aes = await subtle().deriveKey(
        { name: 'PBKDF2', salt: salt as BufferSource, iterations: PKCS12_ITERATIONS, hash: 'SHA-256' },
        base,
        { name: 'AES-CBC', length: 256 },
        false,
        ['encrypt'],
    );
    const ct = new Uint8Array(await subtle().encrypt({ name: 'AES-CBC', iv: iv as BufferSource }, aes, pkcs8 as BufferSource));
    const alg = seq(
        oid(OID_PBES2),
        seq(
            seq(
                oid(OID_PBKDF2),
                seq(octets(salt), integer(PKCS12_ITERATIONS), integer(32), seq(oid(OID_HMAC_SHA256), derNull())),
            ),
            seq(oid(OID_AES256_CBC), octets(iv)),
        ),
    );
    return seq(alg, octets(ct));
}

// ---------------------------------------------------------------------------
// PKCS#12
// ---------------------------------------------------------------------------

const dataContentInfo = (content: Uint8Array): Uint8Array =>
    seq(oid(OID_DATA), explicit0(octets(content)));

/**
 * Builds a password-protected PKCS#12 archive from a PEM certificate (the
 * leaf first; any further CERTIFICATE blocks go in as its chain) and the
 * matching unencrypted private key (PKCS#8, PKCS#1 or SEC1 PEM).
 *
 * @returns the DER-encoded PFX.
 * @throws CertExportError when a PEM block is missing or malformed.
 */
export async function encodePKCS12(certPEM: string, keyPEM: string, passphrase: string): Promise<Uint8Array> {
    const certs = pemBlocks(certPEM).filter((b) => b.label === 'CERTIFICATE');
    if (certs.length === 0) throw new CertExportError('certificate PEM holds no CERTIFICATE block');
    for (const c of certs) {
        const [top] = children(c.der);
        if (!top || top.tag !== 0x30) throw new CertExportError('CERTIFICATE block is not a DER SEQUENCE');
    }
    const pkcs8 = privateKeyInfo(keyPEM);
    const keyId = await digest('SHA-1', certs[0].der);
    const attrs = set(seq(oid(OID_LOCAL_KEY_ID), set(octets(keyId))));

    const certBags = certs.map((c, i) =>
        seq(
            oid(OID_CERT_BAG),
            explicit0(seq(oid(OID_X509_CERTIFICATE), explicit0(octets(c.der)))),
            ...(i === 0 ? [attrs] : []),
        ),
    );
    const keyBag = seq(oid(OID_PKCS8_SHROUDED_KEY_BAG), explicit0(await shroud(pkcs8, passphrase)), attrs);

    const authSafe = seq(dataContentInfo(seq(...certBags)), dataContentInfo(seq(keyBag)));

    const macSalt = randomBytes(16);
    const macKey = await pkcs12KDF(3, bmpPassword(passphrase), macSalt, PKCS12_ITERATIONS);
    const mac = await hmacSHA256(macKey, authSafe);
    const macData = seq(
        seq(seq(oid(OID_SHA256), derNull()), octets(mac)),
        octets(macSalt),
        integer(PKCS12_ITERATIONS),
    );
    return seq(integer(3), dataContentInfo(authSafe), macData);
}

/** {@link encodePKCS12}, base64-encoded as the ExportCertResponse carries it. */
export async function encodePKCS12Base64(certPEM: string, keyPEM: string, passphrase: string): Promise<string> {
    return bytesToBase64(await encodePKCS12(certPEM, keyPEM, passphrase));
}
