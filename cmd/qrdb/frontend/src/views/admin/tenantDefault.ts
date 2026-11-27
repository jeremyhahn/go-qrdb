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

import type { Client } from "@qrdb/sdk";

/**
 * Returns the tenant a create form starts on: "default" when the server
 * lists it, else the first tenant the server lists (System on a fresh
 * daemon), else "" so the picker shows its placeholder.
 */
export async function defaultTenantId(client: Client): Promise<string> {
  try {
    const resp = await client.tenants.list();
    const tenants = resp.tenants ?? [];
    if (tenants.some((t) => t.id === "default")) {
      return "default";
    }
    return tenants[0]?.id ?? "";
  } catch {
    return "";
  }
}

/**
 * Returns the text to show for a failed request: the server's own error
 * text when the response body carries one, else the error message, else
 * the fallback.
 */
export function errorText(err: unknown, fallback: string): string {
  if (!(err instanceof Error)) {
    return typeof err === "string" && err !== "" ? err : fallback;
  }
  const cause = (err as { cause?: unknown }).cause;
  if (cause instanceof Error && cause.message.trim() !== "") {
    const body = cause.message.trim();
    try {
      const parsed: unknown = JSON.parse(body);
      if (parsed !== null && typeof parsed === "object") {
        const obj = parsed as Record<string, unknown>;
        for (const key of ["error", "message", "detail"]) {
          const v = obj[key];
          if (typeof v === "string" && v !== "") {
            return v;
          }
        }
      }
    } catch {
      // Not JSON: the body is the server's text.
    }
    return body;
  }
  return err.message !== "" ? err.message : fallback;
}
