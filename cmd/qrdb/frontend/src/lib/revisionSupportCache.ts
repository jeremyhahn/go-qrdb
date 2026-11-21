// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

/**
 * Remembers which databases' stores answered 501 to the revision probe
 * (GET /kv/revision), so the KV explorer does not ask again on every open.
 *
 * An entry is keyed by the connection profile, the tenant and the database
 * name: two connections (or two tenants) may each hold a database of the
 * same name with different stores. Each entry also stores a fingerprint of
 * the database's backend, shard, mode, data directory and storage codec; a
 * database recreated under the same name with a different fingerprint drops
 * the entry, so the explorer probes again.
 */

/** Where the cache persists its entries (sessionStorage in the app). */
export interface RevisionSupportStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/** Identifies one database on one connection. */
export interface RevisionSupportScope {
  /** Connection profile id (navStore.context.profileId). */
  readonly profileId: string;
  /** Tenant that owns the database; empty when unknown. */
  readonly tenant: string;
  /** Database name. */
  readonly database: string;
}

/** The DatabaseInfo fields that identify one incarnation of a database. */
export interface RevisionSupportFingerprintSource {
  readonly backend?: string;
  readonly shardId?: number;
  readonly mode?: string;
  readonly dataDir?: string;
  readonly storageCodec?: string;
}

/** Session storage key; the name-only cache used "qrdb.noRevisionDatabases". */
export const REVISION_SUPPORT_STORAGE_KEY = "qrdb.noRevisionDatabases.v2";

/** Returns the fingerprint of a database's store identity. */
export function revisionSupportFingerprint(info: RevisionSupportFingerprintSource): string {
  return JSON.stringify([
    info.backend ?? "",
    info.shardId ?? 0,
    info.mode ?? "",
    info.dataDir ?? "",
    info.storageCodec ?? "",
  ]);
}

/** Returns the cache key of scope. */
export function revisionSupportKey(scope: RevisionSupportScope): string {
  return JSON.stringify([scope.profileId, scope.tenant, scope.database]);
}

/** Cache of databases whose store keeps no revisions. */
export class RevisionSupportCache {
  readonly #storage: RevisionSupportStorage | null;
  readonly #storageKey: string;
  readonly #entries: Map<string, string>;

  constructor(storage: RevisionSupportStorage | null, storageKey: string = REVISION_SUPPORT_STORAGE_KEY) {
    this.#storage = storage;
    this.#storageKey = storageKey;
    this.#entries = this.#load();
  }

  /**
   * Reports whether scope's database, as identified by fingerprint, is known
   * to keep no revisions. An entry recorded for a different fingerprint is
   * stale (the database was recreated) and is dropped.
   */
  isUnsupported(scope: RevisionSupportScope, fingerprint: string): boolean {
    const key = revisionSupportKey(scope);
    const stored = this.#entries.get(key);
    if (stored === undefined) return false;
    if (stored === fingerprint) return true;
    this.#entries.delete(key);
    this.#save();
    return false;
  }

  /** Records that scope's database, as identified by fingerprint, keeps no revisions. */
  rememberUnsupported(scope: RevisionSupportScope, fingerprint: string): void {
    this.#entries.set(revisionSupportKey(scope), fingerprint);
    this.#save();
  }

  #load(): Map<string, string> {
    try {
      const raw = this.#storage?.getItem(this.#storageKey);
      const parsed: unknown = raw ? JSON.parse(raw) : {};
      const entries = new Map<string, string>();
      if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
        for (const [k, v] of Object.entries(parsed)) {
          if (typeof v === "string") entries.set(k, v);
        }
      }
      return entries;
    } catch {
      return new Map();
    }
  }

  #save(): void {
    try {
      this.#storage?.setItem(this.#storageKey, JSON.stringify(Object.fromEntries(this.#entries)));
    } catch {
      // Storage unavailable: the in-memory map still covers this page load.
    }
  }
}

function sessionStore(): RevisionSupportStorage | null {
  try {
    return globalThis.sessionStorage ?? null;
  } catch {
    return null;
  }
}

/** The app's cache, kept in sessionStorage so a reload does not ask again. */
export const revisionSupportCache = new RevisionSupportCache(sessionStore());
