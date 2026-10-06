// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Wire contract for WailsClient.invitations: invite() calls straight through
 * to InvitationBinding.Invite over the connected session, and redeem() --
 * the one capability reachable with NO credential at all -- calls
 * InvitationBinding.Redeem with the server/transport/caFile naming its own,
 * one-off connection, exactly as the CLI's --server/--transport/--ca-file do
 * (cmd/qrdb/bindings_invitations.go's RedeemInput).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installInvitationBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { InvitationBinding: binding } } });
}

describe('WailsClient invitations', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('invite() calls InvitationBinding.Invite with the request verbatim', async () => {
        const invite = vi.fn<BindingFn>().mockResolvedValue({
            id: '3',
            token: 'a94e3f1b',
            expiresAt: '2027-01-01T00:00:00Z',
            userID: '9',
            username: 'alice',
            tenantID: 't1',
            created: true,
        });
        installInvitationBinding({ Invite: invite });

        const client = new WailsClient();
        const result = await client.invitations.invite({
            username: 'alice',
            email: 'alice@acme.com',
            roleIDs: ['op-role'],
        });

        expect(invite).toHaveBeenCalledTimes(1);
        expect(invite).toHaveBeenCalledWith({
            username: 'alice',
            email: 'alice@acme.com',
            roleIDs: ['op-role'],
        });
        expect(result.id).toBe('3');
        expect(result.token).toBe('a94e3f1b');
        expect(result.created).toBe(true);
    });

    it('redeem() calls InvitationBinding.Redeem with the connection options and request merged', async () => {
        const redeem = vi.fn<BindingFn>().mockResolvedValue({
            userID: '9',
            username: 'alice',
            tenantID: 't1',
            certPEM: 'CERT',
            thumbprint: 'THUMB',
            notAfter: '2027-01-01T00:00:00Z',
        });
        installInvitationBinding({ Redeem: redeem });

        const client = new WailsClient();
        const result = await client.invitations.redeem(
            { tenantID: 't1', token: 'a94e3f1b', csrPEM: 'CSR', certPEM: 'LEAF', chainPEM: 'CHAIN' },
            { server: 'https://db.example.com', transport: 'rest', caFile: '/tmp/ca.pem' },
        );

        expect(redeem).toHaveBeenCalledTimes(1);
        expect(redeem).toHaveBeenCalledWith({
            server: 'https://db.example.com',
            transport: 'rest',
            caFile: '/tmp/ca.pem',
            tenantID: 't1',
            token: 'a94e3f1b',
            csrPEM: 'CSR',
            certPEM: 'LEAF',
            chainPEM: 'CHAIN',
        });
        expect(result.certPEM).toBe('CERT');
        expect(result.userID).toBe('9');
    });

    it('redeem() sends empty connection fields when no options are given', async () => {
        const redeem = vi.fn<BindingFn>().mockResolvedValue({
            userID: '9',
            username: 'alice',
            tenantID: 't1',
            certPEM: 'CERT',
            thumbprint: 'THUMB',
            notAfter: '2027-01-01T00:00:00Z',
        });
        installInvitationBinding({ Redeem: redeem });

        const client = new WailsClient();
        await client.invitations.redeem({ tenantID: 't1', token: 'tok', csrPEM: 'CSR' });

        expect(redeem).toHaveBeenCalledWith(
            expect.objectContaining({ server: '', transport: '', caFile: '' }),
        );
    });
});
