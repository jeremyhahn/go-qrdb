// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Global teardown for the real-backend Playwright suite.
 * Kills the shared daemon process and removes the temporary data directory.
 */

import { shutdownDaemon, shutdownMvccDaemon } from "./fixtures/daemon.ts";

async function globalTeardown(): Promise<void> {
  await shutdownDaemon();
  await shutdownMvccDaemon();
}

export default globalTeardown;
