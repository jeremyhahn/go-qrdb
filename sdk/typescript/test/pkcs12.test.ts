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
import { CertExportError } from '../src/errors.js';

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
    });
    afterAll(() => rmSync(dir, { recursive: true, force: true }));

    const read = (f: string): string => readFileSync(join(dir, f), 'utf8');

    it.each([
        ['PKCS#8 EC key', 'cert.pem', 'key.pem'],
        ['SEC1 EC key', 'cert.pem', 'sec1.pem'],
        ['PKCS#1 RSA key', 'rsacert.pem', 'rsa1.pem'],
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
