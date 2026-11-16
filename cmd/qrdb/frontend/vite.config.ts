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

import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { API_PREFIXES } from "./api-routes.ts";
import { PROXY_CLIENT_TLS_DIR_ENV, TLS_CERT_ENV, TLS_KEY_ENV } from "./playwright.tls.ts";
import { readFileSync } from "node:fs";
import { Agent } from "node:https";
import { join } from "node:path";

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The app entry chunk is split from third-party deps (manualChunks below).
    // The remaining vendor chunk is inherent dependency size; this is a Wails
    // desktop app (assets loaded from disk, not over the network), so the
    // default 500 kB network-oriented warning does not apply to the vendor
    // chunk. App code is kept separate so regressions in our own bundle size
    // remain visible under the default threshold.
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      output: {
        // Split third-party dependencies into a separate vendor chunk so the
        // app entry chunk stays well under Vite's 500 kB warning threshold.
        manualChunks(id) {
          if (id.includes("node_modules")) {
            return "vendor";
          }
        },
      },
    },
  },
  resolve: {
    alias: {
      $lib: "/src/lib",
      $stores: "/src/stores",
      $components: "/src/components",
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    // The API server the dev/E2E proxy forwards to. Defaults to the
    // conventional local admin port for interactive use; every Playwright tier
    // overrides it via QRDB_PROXY_TARGET (the mocked tier with a per-run port
    // nothing listens on, the DOM tier with its daemon's admin port).
    proxy: buildApiProxy(process.env.QRDB_PROXY_TARGET ?? "https://localhost:63002"),
    // `vite preview` inherits this, so both servers take the same TLS.
    https: serverTLS(),
  },
});

/**
 * serverTLS returns the certificate and key the Playwright tiers generate per
 * run (playwright.tls.ts), so the app is served over https as qrdbd serves web
 * mode: every secret-bearing SDK call refuses an http origin. Unset, the
 * server speaks plain http, for an interactive `npm run dev` only.
 */
function serverTLS(): { cert: string; key: string } | undefined {
  const cert = process.env[TLS_CERT_ENV];
  const key = process.env[TLS_KEY_ENV];
  if (cert && key) {
    return { cert, key };
  }
  if (cert || key) {
    throw new Error(`${TLS_CERT_ENV} and ${TLS_KEY_ENV} must be set together`);
  }
  return undefined;
}

/**
 * clientCertAgent is the proxy's agent when PROXY_CLIENT_TLS_DIR_ENV is set:
 * on every new connection it presents the client certificate found in that
 * directory (client.crt, client.key), or none when they are absent, so a DOM
 * spec can switch the UI's principal per test without restarting Vite. No
 * keep-alive and no TLS session reuse: a connection never outlives the
 * credential it was opened with.
 */
function clientCertAgent(dir: string): Agent {
  const agent = new Agent({ keepAlive: false, maxCachedSessions: 0 });
  const connect = agent.createConnection.bind(agent);
  agent.createConnection = ((options: Record<string, unknown>, cb: unknown) => {
    let cred: { cert: Buffer; key: Buffer } | Record<string, never> = {};
    try {
      cred = { cert: readFileSync(join(dir, "client.crt")), key: readFileSync(join(dir, "client.key")) };
    } catch {
      // No credential: an unauthenticated connection.
    }
    return (connect as (o: unknown, c: unknown) => unknown)({ ...options, ...cred }, cb);
  }) as typeof agent.createConnection;
  return agent;
}

/**
 * buildApiProxy maps every server-side API route prefix (api-routes.ts) to the given backend
 * target. `secure: false` accepts the daemon's self-signed TLS certificate.
 */
function buildApiProxy(target: string): Record<string, { target: string; secure: boolean; agent?: Agent }> {
  const dir = process.env[PROXY_CLIENT_TLS_DIR_ENV];
  const agent = dir ? clientCertAgent(dir) : undefined;
  const proxy: Record<string, { target: string; secure: boolean; agent?: Agent }> = {};
  for (const p of API_PREFIXES) {
    proxy[p] = agent ? { target, secure: false, agent } : { target, secure: false };
  }
  return proxy;
}
