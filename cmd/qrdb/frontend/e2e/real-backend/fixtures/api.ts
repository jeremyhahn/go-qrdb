// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Typed API helper layer for real-backend specs.
 *
 * Wraps Playwright's APIRequestContext with typed methods that map to the
 * qrdbd REST API. All requests use ignoreHTTPSErrors-aware fetch via the
 * Playwright request context passed in, so no manual TLS configuration is
 * required in specs.
 */

import type { APIRequestContext } from "@playwright/test";

// ---------------------------------------------------------------------------
// Low-level helpers
// ---------------------------------------------------------------------------

export class ApiClient {
  constructor(
    private readonly request: APIRequestContext,
    readonly baseUrl: string,
  ) {}

  /** GET <baseUrl><path> — returns parsed JSON. */
  async get<T>(path: string, params?: Record<string, string>): Promise<T> {
    const url = new URL(this.baseUrl + path);
    if (params) {
      for (const [k, v] of Object.entries(params)) {
        url.searchParams.set(k, v);
      }
    }
    const resp = await this.request.get(url.toString());
    if (!resp.ok()) {
      const body = await resp.text();
      throw new Error(`GET ${path} returned ${resp.status()}: ${body}`);
    }
    return resp.json() as Promise<T>;
  }

  /** POST <baseUrl><path> with JSON body — returns parsed JSON. */
  async post<T>(path: string, body: unknown): Promise<T> {
    const resp = await this.request.post(this.baseUrl + path, {
      data: body,
      headers: { "Content-Type": "application/json" },
    });
    if (!resp.ok()) {
      const text = await resp.text();
      throw new Error(`POST ${path} returned ${resp.status()}: ${text}`);
    }
    return resp.json() as Promise<T>;
  }

  /** PUT <baseUrl><path> with JSON body — returns parsed JSON. */
  async put<T>(path: string, body: unknown): Promise<T> {
    const resp = await this.request.put(this.baseUrl + path, {
      data: body,
      headers: { "Content-Type": "application/json" },
    });
    if (!resp.ok()) {
      const text = await resp.text();
      throw new Error(`PUT ${path} returned ${resp.status()}: ${text}`);
    }
    return resp.json() as Promise<T>;
  }

  /** PATCH <baseUrl><path> with JSON body — returns parsed JSON. */
  async patch<T>(path: string, body: unknown): Promise<T> {
    const resp = await this.request.patch(this.baseUrl + path, {
      data: body,
      headers: { "Content-Type": "application/json" },
    });
    if (!resp.ok()) {
      const text = await resp.text();
      throw new Error(`PATCH ${path} returned ${resp.status()}: ${text}`);
    }
    return resp.json() as Promise<T>;
  }

  /** PUT <baseUrl><path> with plain text body — used for KV writes. */
  async putText(path: string, value: string): Promise<void> {
    const resp = await this.request.put(this.baseUrl + path, {
      data: value,
      headers: { "Content-Type": "text/plain" },
    });
    if (!resp.ok()) {
      const text = await resp.text();
      throw new Error(`PUT ${path} returned ${resp.status()}: ${text}`);
    }
  }

  /** DELETE <baseUrl><path> — asserts 204. */
  async delete(path: string): Promise<void> {
    const resp = await this.request.delete(this.baseUrl + path);
    if (!resp.ok()) {
      const text = await resp.text();
      throw new Error(`DELETE ${path} returned ${resp.status()}: ${text}`);
    }
  }

  /** Returns the raw HTTP status for a GET request (does not throw). */
  async getStatus(path: string): Promise<number> {
    const resp = await this.request.get(this.baseUrl + path);
    return resp.status();
  }
}

// ---------------------------------------------------------------------------
// Typed API response shapes (subset used in specs)
// ---------------------------------------------------------------------------

export interface TenantInfo {
  id: string;
  name: string;
  isolation_type: string;
  createdAt: string;
  updatedAt: string;
}

export interface ListTenantsResponse {
  tenants: TenantInfo[];
  total: number;
}

export interface DatabaseInfo {
  name: string;
  engine: string;
  backend: string;
  status: string;
  protocol?: { mysql: boolean; postgresql: boolean };
}

export interface ListDatabasesResponse {
  databases: DatabaseInfo[];
}

export interface KVListResponse {
  keys: string[];
}

export interface TransactionResponse {
  succeeded: boolean;
  revision: number;
  responses: unknown[];
}

export interface BatchResponse {
  responses: { key: string; value: string }[];
}

export interface IndexQueryResponse {
  keys: string[];
}

export interface LeaseInfo {
  id: number;
  ttl: number;
  granted_ttl: number;
  remaining_ttl: number;
}

export interface LeaseListResponse {
  leases: LeaseInfo[];
}

export interface HealthResponse {
  status: string;
}
