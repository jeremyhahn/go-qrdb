// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// ---------------------------------------------------------------------------
// Audit TypeScript SDK
//
// Provides typed request/response types and a client for querying compliance-
// grade audit events via the QRDB REST API.
//
// Authorization: requires "audit:query" permission.
// TODO: switch to AuditQuery permission constant once Phase 2 lands.
// ---------------------------------------------------------------------------

import { deepSnakeToCamel, deepCamelToSnake } from './transform.js';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** A single compliance-grade audit event. */
export interface AuditEvent {
    /** UTC timestamp of the event. */
    readonly timestamp: string;
    /** Actor identity (cert CN, JWT sub, API key). */
    readonly actor: string;
    /** Tenant that owns the resource (empty for cluster-level ops). */
    readonly tenantID?: string;
    /** Role of the actor at event time. */
    readonly role?: string;
    /** Resource identifier (e.g. "barrier", "user:alice"). */
    readonly resource: string;
    /** Action performed (e.g. "initialize", "unseal"). */
    readonly action: string;
    /** Outcome: "success", "failure:<reason>", or "denied:<reason>". */
    readonly outcome: string;
    /** Optional context attributes. */
    readonly attributes?: Record<string, unknown>;
    /** Client IP address. */
    readonly clientIP?: string;
    /** Request trace ID. */
    readonly requestID?: string;
    /** SHA-256 hex of the previous event (hash-chain link). */
    readonly prevHash?: string;
}

/** Filter for querying audit events. */
export interface AuditFilter {
    /** Start time (inclusive), ISO 8601 / RFC 3339. */
    from?: string;
    /** End time (exclusive), ISO 8601 / RFC 3339. */
    to?: string;
    /** Actor identity filter. */
    actor?: string;
    /** Resource filter. */
    resource?: string;
    /** Action filter. */
    action?: string;
    /** Outcome prefix filter ("success" or "failure"). */
    outcome?: string;
    /** Tenant ID filter. */
    tenantId?: string;
    /** Max events per page (default 100, max 1000). */
    limit?: number;
    /** Opaque pagination cursor from previous AuditPage.next. */
    cursor?: string;
}

/** Paginated audit query response. */
export interface AuditPage {
    /** Matching audit events in chronological order. */
    readonly events: AuditEvent[];
    /** Opaque cursor for the next page (empty when exhausted). */
    readonly next?: string;
    /** Total matching events, or -1 if unknown. */
    readonly total: number;
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

/** AuditClient queries audit events from the QRDB REST API. */
export class AuditClient {
    private readonly baseUrl: string;
    private readonly headers: Record<string, string>;

    constructor(baseUrl: string, headers: Record<string, string> = {}) {
        this.baseUrl = baseUrl.replace(/\/$/, '');
        this.headers = headers;
    }

    /**
     * Query audit events matching the provided filter.
     * Maps to GET /v1/audit/events.
     * Authorization: requires "audit:query" permission.
     */
    async query(f: AuditFilter = {}): Promise<AuditPage> {
        const snake = deepCamelToSnake(f) as Record<string, unknown>;
        const params = new URLSearchParams();
        for (const [k, v] of Object.entries(snake)) {
            if (v !== undefined && v !== null && v !== '') {
                params.set(k, String(v));
            }
        }

        const url = `${this.baseUrl}/v1/audit/events` +
            (params.size > 0 ? `?${params.toString()}` : '');

        const resp = await fetch(url, {
            method: 'GET',
            headers: { 'Accept': 'application/json', ...this.headers },
        });

        if (!resp.ok) {
            const body = await resp.text();
            throw new Error(`audit query failed: ${resp.status} ${body}`);
        }

        const raw = await resp.json() as unknown;
        return deepSnakeToCamel(raw) as AuditPage;
    }
}
