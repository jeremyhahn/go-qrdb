// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Watch events real-backend spec — regression coverage for bug #9 (prefix
 * watch) and bug #16 (PUT events labeled as "PUT" not "DELETE").
 *
 * Uses the SSE endpoint GET /watch?key=<prefix>&prefix=true and opens an
 * HTTPS connection via Node's built-in `https` module (self-signed cert
 * accepted). A PUT to a matching key triggers an event that must carry
 * type="PUT".
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";
import * as https from "https";
import * as http from "http";

function key(label: string): string {
  return `e2e-watch-${label}-${Date.now()}`;
}

/**
 * Opens an HTTPS SSE connection and collects events until either:
 *   - One complete SSE "data:" frame is received, or
 *   - The timeoutMs expires.
 *
 * Returns the first complete SSE frame string (everything after "data: "),
 * or null if no frame was received within the timeout.
 */
function collectOneSseFrame(
  url: string,
  timeoutMs: number,
): Promise<string | null> {
  return new Promise((resolve) => {
    const parsed = new URL(url);
    let buffer = "";
    let resolved = false;

    const timer = setTimeout(() => {
      if (!resolved) {
        resolved = true;
        req.destroy();
        resolve(null);
      }
    }, timeoutMs);

    const options: https.RequestOptions = {
      hostname: parsed.hostname,
      port: Number(parsed.port),
      path: parsed.pathname + parsed.search,
      method: "GET",
      rejectUnauthorized: false,
      headers: {
        Accept: "text/event-stream",
        "Cache-Control": "no-cache",
      },
    };

    const req = https.request(options, (res: http.IncomingMessage) => {
      res.setEncoding("utf8");
      res.on("data", (chunk: string) => {
        buffer += chunk;
        const frameEnd = buffer.indexOf("\n\n");
        if (frameEnd !== -1 && !resolved) {
          resolved = true;
          clearTimeout(timer);
          req.destroy();
          const frame = buffer.slice(0, frameEnd);
          resolve(frame);
        }
      });
      res.on("end", () => {
        if (!resolved) {
          resolved = true;
          clearTimeout(timer);
          resolve(null);
        }
      });
      res.on("error", () => {
        if (!resolved) {
          resolved = true;
          clearTimeout(timer);
          resolve(null);
        }
      });
    });

    req.on("error", (err: Error) => {
      // Ignore ECONNRESET from our own destroy() call
      if (!resolved && (err as NodeJS.ErrnoException).code !== "ECONNRESET") {
        resolved = true;
        clearTimeout(timer);
        resolve(null);
      }
    });

    req.end();
  });
}

test.describe("Watch events (real backend — bug #9, #16)", () => {
  test(
    "prefix watch receives a PUT event with type=PUT when matching key is written",
    async ({ request, daemon }) => {
      const api = new ApiClient(request, daemon.adminUrl);
      const prefix = `e2e-watch-prefix-${Date.now()}/`;
      const writeKey = `${prefix}trigger`;

      const watchUrl = `${daemon.adminUrl}/watch?key=${encodeURIComponent(prefix)}&prefix=true`;

      // Start collecting the SSE stream (5 second timeout for the event)
      const framePromise = collectOneSseFrame(watchUrl, 5_000);

      // Give the watch connection time to establish before triggering the event
      await new Promise((r) => setTimeout(r, 400));

      // Write a key matching the prefix — triggers the watch event
      await api.putText(`/kv/${encodeURIComponent(writeKey)}`, "watch-value");

      const frameRaw = await framePromise;

      // Contract: watch MUST deliver an SSE frame within the timeout.
      // A null here means the watch pipeline is broken — fail loudly.
      expect(
        frameRaw,
        "watch SSE frame did not arrive within timeout (watch pipeline broken)",
      ).not.toBeNull();

      const dataLine = frameRaw!
        .split("\n")
        .find((l) => l.startsWith("data:"));
      expect(dataLine).toBeDefined();

      const payload = JSON.parse(dataLine!.slice("data:".length).trim()) as {
        events?: Array<{ type: string; kv?: { key: string } }>;
      };

      expect(Array.isArray(payload.events)).toBe(true);
      // Watch event keys/values are []byte on the wire, so encoding/json emits
      // them base64 (RFC 4648) — the same binary-safe representation the KV GET
      // responses use. Decode before comparing to the plaintext key.
      const evt = payload.events!.find(
        (e) => e.kv?.key && Buffer.from(e.kv.key, "base64").toString() === writeKey,
      );
      expect(evt, `event for ${writeKey} missing in payload`).toBeDefined();
      // Bug #16 regression: event type must be "PUT" not "DELETE"
      expect(evt!.type).toBe("PUT");

      // Cleanup
      await api.delete(`/kv/${encodeURIComponent(writeKey)}`);
    },
  );

  test(
    "non-prefix watch receives PUT event for exact key",
    async ({ request, daemon }) => {
      const api = new ApiClient(request, daemon.adminUrl);
      const exactKey = key("exact");

      const watchUrl = `${daemon.adminUrl}/watch?key=${encodeURIComponent(exactKey)}`;

      const framePromise = collectOneSseFrame(watchUrl, 5_000);

      await new Promise((r) => setTimeout(r, 400));

      await api.putText(`/kv/${encodeURIComponent(exactKey)}`, "exact-val");

      const frameRaw = await framePromise;

      // Contract: watch MUST deliver an SSE frame within the timeout.
      expect(
        frameRaw,
        "exact-key watch SSE frame did not arrive within timeout",
      ).not.toBeNull();

      const dataLine = frameRaw!
        .split("\n")
        .find((l) => l.startsWith("data:"));
      expect(dataLine).toBeDefined();
      const payload = JSON.parse(
        dataLine!.slice("data:".length).trim(),
      ) as { events?: Array<{ type: string }> };
      const evt = payload.events?.[0];
      expect(evt, "expected exactly one event on exact-key watch").toBeDefined();
      // Bug #16: must be "PUT" not "DELETE"
      expect(evt!.type).toBe("PUT");

      await api.delete(`/kv/${encodeURIComponent(exactKey)}`);
    },
  );
});
