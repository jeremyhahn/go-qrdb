<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.

  StepAdminCredential.svelte
  ===========================
  Resolves the bootstrap admin's certificate, the same call that brings up
  the barrier (ceremony.initialize) issues it in: exactly one of a CSR
  (the admin's key never leaves the machine that generated it), a
  bring-your-own certificate the admin already holds, or -- dev/test/
  evaluation ONLY -- server-generated. The generated path's private key is
  never shown on this page: the desktop app saves it to a local file from
  Go and reports only where (see ServerInitWizard's "complete" step).
-->
<script lang="ts">
  export type AdminCredentialMode = "csr" | "byoc" | "generate";

  let {
    mode = $bindable("csr"),
    csrPem = $bindable(""),
    certPem = $bindable(""),
    certPoolPem = $bindable(""),
    nextLabel = "Initialize",
    onNext,
    onBack,
  }: {
    mode?: AdminCredentialMode;
    csrPem?: string;
    certPem?: string;
    certPoolPem?: string;
    /**
     * Label for the forward button. Callers whose wizard still has a
     * review step after this one (ConnectionManager's embedded-server
     * creation wizard) pass "Next" -- onNext only advances the step there,
     * it does not itself call ceremony.initialize. Defaults to
     * "Initialize" for callers (ServerInitWizard) where onNext IS the
     * ceremony.initialize call.
     */
    nextLabel?: string;
    onNext: () => void;
    onBack: () => void;
  } = $props();

  let isValid = $derived(
    mode === "csr"
      ? csrPem.trim() !== ""
      : mode === "byoc"
        ? certPem.trim() !== ""
        : true,
  );
</script>

<div class="flex flex-col gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Bootstrap Admin Credential</h2>
    <p class="text-base-content/70 mt-2 text-sm">
      The server issues the bootstrap admin's certificate in the same call that
      brings up the barrier. Choose exactly one path.
    </p>
  </div>

  <div class="flex flex-col gap-2">
    <label
      class="flex items-start gap-3 rounded-lg border p-3 cursor-pointer"
      class:border-primary={mode === "csr"}
    >
      <input
        type="radio"
        class="radio radio-sm mt-1"
        name="admin-credential-mode"
        value="csr"
        checked={mode === "csr"}
        onchange={() => (mode = "csr")}
        data-testid="admin-mode-csr"
      />
      <span>
        <span class="font-semibold text-sm">CSR (recommended)</span>
        <span class="text-base-content/60 block text-xs"
          >The admin's key never leaves the machine that generated it.</span
        >
      </span>
    </label>
    <label
      class="flex items-start gap-3 rounded-lg border p-3 cursor-pointer"
      class:border-primary={mode === "byoc"}
    >
      <input
        type="radio"
        class="radio radio-sm mt-1"
        name="admin-credential-mode"
        value="byoc"
        checked={mode === "byoc"}
        onchange={() => (mode = "byoc")}
        data-testid="admin-mode-byoc"
      />
      <span>
        <span class="font-semibold text-sm">Bring your own certificate</span>
        <span class="text-base-content/60 block text-xs"
          >Adopt a certificate the admin already holds. Nothing is issued.</span
        >
      </span>
    </label>
    <label
      class="flex items-start gap-3 rounded-lg border p-3 cursor-pointer"
      class:border-primary={mode === "generate"}
    >
      <input
        type="radio"
        class="radio radio-sm mt-1"
        name="admin-credential-mode"
        value="generate"
        checked={mode === "generate"}
        onchange={() => (mode = "generate")}
        data-testid="admin-mode-generate"
      />
      <span>
        <span class="font-semibold text-sm"
          >Generate (dev/test/evaluation ONLY)</span
        >
        <span class="text-base-content/60 block text-xs"
          >The server generates a key pair. NOT suitable for production.</span
        >
      </span>
    </label>
  </div>

  {#if mode === "csr"}
    <div class="form-control">
      <label class="label" for="admin-csr-pem">
        <span class="label-text text-xs">Admin CSR PEM (generated offline)</span
        >
      </label>
      <textarea
        id="admin-csr-pem"
        class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
        rows={6}
        placeholder={"-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----"}
        value={csrPem}
        oninput={(e: Event) =>
          (csrPem = (e.currentTarget as HTMLTextAreaElement).value)}
        data-testid="admin-csr-pem"></textarea>
    </div>
  {:else if mode === "byoc"}
    <div class="form-control">
      <label class="label" for="admin-cert-pem">
        <span class="label-text text-xs">Admin certificate PEM</span>
      </label>
      <textarea
        id="admin-cert-pem"
        class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
        rows={6}
        placeholder={"-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----"}
        value={certPem}
        oninput={(e: Event) =>
          (certPem = (e.currentTarget as HTMLTextAreaElement).value)}
        data-testid="admin-cert-pem"></textarea>
    </div>
    <div class="form-control">
      <label class="label" for="admin-cert-pool-pem">
        <span class="label-text text-xs"
          >Trust anchor bundle (optional; only if not issued by this
          deployment's own CA)</span
        >
      </label>
      <textarea
        id="admin-cert-pool-pem"
        class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
        rows={4}
        value={certPoolPem}
        oninput={(e: Event) =>
          (certPoolPem = (e.currentTarget as HTMLTextAreaElement).value)}
        data-testid="admin-cert-pool-pem"></textarea>
    </div>
  {:else}
    <div
      role="alert"
      class="alert alert-warning text-sm"
      data-testid="admin-generate-warning"
    >
      <span>
        <strong>Insecure, dev/test/evaluation ONLY.</strong> The server will generate
        the admin's key pair in-process. This does NOT meet FedRAMP, HIPAA or PCI
        DSS 4.0. The generated private key is saved to a local file by the desktop
        app -- never shown on this page -- and cannot be retrieved again afterward.
      </span>
    </div>
  {/if}

  <div class="flex justify-between mt-2">
    <button class="btn btn-ghost" onclick={onBack} data-testid="admin-back-btn"
      >Back</button
    >
    <button
      class="btn btn-primary"
      onclick={onNext}
      disabled={!isValid}
      data-testid="admin-next-btn"
    >
      {nextLabel}
    </button>
  </div>
</div>
