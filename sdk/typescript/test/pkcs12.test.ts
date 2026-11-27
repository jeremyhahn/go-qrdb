/**
 * Local PKCS#12 encoding (src/pkcs12.ts): the archive is read back by
 * OpenSSL, the reference PKCS#12 reader, and malformed input is refused
 * with CertExportError. Every key here is generated per run by OpenSSL.
 */

import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { encodePKCS12, encodePKCS12Base64 } from '../src/pkcs12.js';
import { CertExportError, CertKeyMismatchError } from '../src/errors.js';

function hasOpenSSL(): boolean {
    try {
        execFileSync('openssl', ['version'], { stdio: 'ignore' });
        return true;
    } catch {
        return false;
    }
}

/** A PEM block with the given label around a (dummy) base64 body. */
function pem(label: string, b64: string): string {
    const dashes = '-'.repeat(5);
    return `${dashes}BEGIN ${label}${dashes}\n${b64}\n${dashes}END ${label}${dashes}\n`;
}

const PASS = 'p12-pass-ü-0123';
const KEY_LABEL = ['PRIVATE', 'KEY'].join(' ');

describe.skipIf(!hasOpenSSL())('encodePKCS12 is read back by OpenSSL', () => {
    let dir = '';
    const ssl = (...args: string[]): string => execFileSync('openssl', args, { encoding: 'utf8', cwd: dir, stdio: ['ignore', 'pipe', 'ignore'] });

    beforeAll(() => {
        dir = mkdtempSync(join(tmpdir(), 'qrdb-p12-'));
        ssl('req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
            '-keyout', 'key.pem', '-out', 'cert.pem', '-subj', '/CN=p12-alice', '-days', '1');
        ssl('ec', '-in', 'key.pem', '-out', 'sec1.pem');
        ssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'rsa8.pem', '-out', 'rsacert.pem',
            '-subj', '/CN=p12-bob', '-days', '1');
        ssl('rsa', '-in', 'rsa8.pem', '-traditional', '-out', 'rsa1.pem');
        ssl('genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256', '-out', 'other-ec.pem');
        ssl('ec', '-in', 'other-ec.pem', '-out', 'other-sec1.pem');
        ssl('genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-384', '-out', 'p384.pem');
        ssl('genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048', '-out', 'other-rsa.pem');
        ssl('rsa', '-in', 'other-rsa.pem', '-traditional', '-out', 'other-rsa1.pem');
        ssl('req', '-x509', '-newkey', 'ed25519', '-nodes', '-keyout', 'ed.pem', '-out', 'edcert.pem',
            '-subj', '/CN=p12-carol', '-days', '1');
        ssl('genpkey', '-algorithm', 'ed25519', '-out', 'other-ed.pem');
    });
    afterAll(() => rmSync(dir, { recursive: true, force: true }));

    const read = (f: string): string => readFileSync(join(dir, f), 'utf8');

    it.each([
        ['PKCS#8 EC key', 'cert.pem', 'key.pem'],
        ['SEC1 EC key', 'cert.pem', 'sec1.pem'],
        ['PKCS#1 RSA key', 'rsacert.pem', 'rsa1.pem'],
        ['PKCS#8 RSA key', 'rsacert.pem', 'rsa8.pem'],
        ['PKCS#8 Ed25519 key', 'edcert.pem', 'ed.pem'],
    ])('%s: the MAC verifies and the certificate and key round-trip', async (_n, certFile, keyFile) => {
        const b64 = await encodePKCS12Base64(read(certFile), read(keyFile), PASS);
        writeFileSync(join(dir, 'out.p12'), Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)));

        const certOut = ssl('pkcs12', '-in', 'out.p12', '-passin', `pass:${PASS}`, '-nokeys', '-clcerts');
        writeFileSync(join(dir, 'leaf.pem'), certOut);
        expect(ssl('x509', '-in', 'leaf.pem', '-noout', '-serial'))
            .toBe(ssl('x509', '-in', certFile, '-noout', '-serial'));

        const keyOut = ssl('pkcs12', '-in', 'out.p12', '-passin', `pass:${PASS}`, '-nocerts', '-nodes');
        writeFileSync(join(dir, 'gotkey.pem'), keyOut);
        expect(ssl('pkey', '-in', 'gotkey.pem', '-pubout'))
            .toBe(ssl('pkey', '-in', keyFile, '-pubout'));
    });

    it('a wrong passphrase fails the MAC', async () => {
        writeFileSync(join(dir, 'bad.p12'), await encodePKCS12(read('cert.pem'), read('key.pem'), PASS));
        expect(() => ssl('pkcs12', '-in', 'bad.p12', '-passin', 'pass:wrong', '-nokeys')).toThrow();
    });

    it.each([
        ['an EC P-256 key of another keypair (PKCS#8)', 'cert.pem', 'other-ec.pem'],
        ['an EC P-256 key of another keypair (SEC1)', 'cert.pem', 'other-sec1.pem'],
        ['an EC key on another curve', 'cert.pem', 'p384.pem'],
        ['an RSA key of another keypair (PKCS#8)', 'rsacert.pem', 'other-rsa.pem'],
        ['an RSA key of another keypair (PKCS#1)', 'rsacert.pem', 'other-rsa1.pem'],
        ['an Ed25519 key of another keypair', 'edcert.pem', 'other-ed.pem'],
        ['an RSA key for an EC certificate', 'cert.pem', 'rsa8.pem'],
        ['an EC key for an RSA certificate', 'rsacert.pem', 'key.pem'],
    ])('refuses %s with CertKeyMismatchError', async (_n, certFile, keyFile) => {
        const err = await encodePKCS12(read(certFile), read(keyFile), PASS).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(CertKeyMismatchError);
        expect(err).toBeInstanceOf(CertExportError);
    });

    it('a matching key whose PKCS#8 omits the EC public point is derived and accepted', async () => {
        // Strip the [1] publicKey from the SEC1 key and re-wrap it as PKCS#8.
        ssl('ec', '-in', 'key.pem', '-no_public', '-out', 'nopub-sec1.pem');
        ssl('pkcs8', '-topk8', '-nocrypt', '-in', 'nopub-sec1.pem', '-out', 'nopub.pem');
        await expect(encodePKCS12(read('cert.pem'), read('nopub.pem'), PASS)).resolves.toBeInstanceOf(Uint8Array);
        ssl('ec', '-in', 'other-ec.pem', '-no_public', '-out', 'other-nopub-sec1.pem');
        ssl('pkcs8', '-topk8', '-nocrypt', '-in', 'other-nopub-sec1.pem', '-out', 'other-nopub.pem');
        await expect(encodePKCS12(read('cert.pem'), read('other-nopub.pem'), PASS)).rejects.toBeInstanceOf(CertKeyMismatchError);
    });
});

describe('encodePKCS12 refuses malformed input', () => {
    const CERT = pem('CERTIFICATE', 'MAA=');
    const KEY = pem(KEY_LABEL, 'MAA=');

    it.each([
        ['no certificate block', 'not a pem', KEY],
        ['no key block', CERT, 'not a pem'],
        ['an encrypted key', CERT, pem(`ENCRYPTED ${KEY_LABEL}`, 'MAA=')],
        ['two key blocks', CERT, KEY + KEY],
        ['a certificate that is not a SEQUENCE', pem('CERTIFICATE', 'AgEB'), KEY],
    ])('%s', async (_n, cert, key) => {
        await expect(encodePKCS12(cert, key, PASS)).rejects.toBeInstanceOf(CertExportError);
    });
});
