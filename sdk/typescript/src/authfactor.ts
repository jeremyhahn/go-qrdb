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
// Auth Factor TypeScript SDK
//
// Provides typed request/response types and a client for custodian auth factor
// enrollment, challenge issuance, verification, listing, and revocation via
// the QRDB REST API.
//
// Supported factor types: pkcs11, piv, fido2
//
// Authorization: TODO — apply permission constants once Phase 2 ships.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Factor enrollment record. */
export interface Enrollment {
    /** Opaque enrollment identifier. */
    readonly enrollmentId: string;
    /** Auth factor type (pkcs11, piv, fido2). */
    readonly factorName: string;
    /** Enrolled user or custodian identifier. */
    readonly userId: string;
    /** Tenant scope for multi-tenant deployments (empty for cluster-level). */
    readonly tenantId?: string;
    /** Factor-specific enrollment metadata (e.g. credential ID for FIDO2). */
    readonly metadata?: Record<string, unknown>;
    /** When the enrollment was recorded (RFC 3339). */
    readonly enrolledAt: string;
    /** When the enrollment was revoked (RFC 3339); present only for revoked enrollments. */
    readonly revokedAt?: string;
}

/** Request to enroll a user with an auth factor. */
export interface EnrollFactorRequest {
    /** Auth factor type: pkcs11, piv, or fido2. */
    factorName: string;
    /** User or custodian identifier being enrolled. */
    subject: string;
    /** Optional tenant scope. */
    tenantId?: string;
    /** Factor-specific enrollment parameters. */
    methodParams?: Record<string, unknown>;
}

/** Response after successful enrollment. */
export type EnrollFactorResponse = Enrollment;

/** Request to issue a challenge for an enrollment. */
export interface ChallengeFactorRequest {
    /** The enrollment ID to challenge. */
    enrollmentId: string;
}

/** Challenge response returned to the caller. */
export interface ChallengeFactorResponse {
    /** Opaque challenge identifier. */
    readonly challengeId: string;
    /** The enrollment this challenge is bound to. */
    readonly enrollmentId: string;
    /** Random nonce bytes the custodian must sign (base64). */
    readonly nonce: string;
    /** Challenge expiry time (RFC 3339). */
    readonly expiresAt: string;
    /** Factor-specific challenge data (e.g. WebAuthn options for FIDO2). */
    readonly methodData?: Record<string, unknown>;
}

/** Request to verify a challenge response. */
export interface VerifyFactorRequest {
    /** Challenge ID issued by challengeFactor. */
    challengeId: string;
    /** Optional factor type override. */
    factorName?: string;
    /** Signed response or proof (base64-encoded bytes). */
    value: string;
    /** Factor-specific response metadata. */
    metadata?: Record<string, string>;
}

/** Request to list enrolled factors (optional subject filter). */
export interface ListFactorsRequest {
    /** Filter enrollments by user or custodian identifier. */
    subject?: string;
}

/** Response for a factor list query. */
export interface ListFactorsResponse {
    /** Matching enrollment records. */
    readonly enrollments: Enrollment[];
    /** Total number of enrollments returned. */
    readonly total: number;
}

/** Request to revoke an enrollment. */
export interface RevokeFactorRequest {
    /** Enrollment ID to revoke. */
    enrollmentId: string;
}

// ---------------------------------------------------------------------------
// AuthFactorClient
// ---------------------------------------------------------------------------

/**
 * AuthFactorClient provides typed operations for custodian auth factor
 * enrollment, challenge issuance, verification, listing, and revocation.
 *
 * Supported factor types: pkcs11, piv, fido2
 */
export class AuthFactorClient {
    private readonly baseUrl: string;
    private readonly headers: Record<string, string>;

    constructor(baseUrl: string, headers: Record<string, string> = {}) {
        this.baseUrl = baseUrl.replace(/\/$/, '');
        this.headers = headers;
    }

    /**
     * Enroll a custodian in the given auth factor type.
     * Maps to POST /v1/authfactors/{factor_name}/enroll.
     */
    async enrollFactor(req: EnrollFactorRequest): Promise<EnrollFactorResponse> {
        const resp = await fetch(
            `${this.baseUrl}/v1/authfactors/${encodeURIComponent(req.factorName)}/enroll`,
            {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', ...this.headers },
                body: JSON.stringify({
                    factor_name: req.factorName,
                    subject: req.subject,
                    tenant_id: req.tenantId,
                    method_params: req.methodParams,
                }),
            }
        );

        if (!resp.ok) {
            const body = await resp.text();
            throw new AuthFactorError('EnrollFactor', resp.status, body);
        }

        const raw = await resp.json() as RawEnrollment;
        return toEnrollment(raw);
    }

    /**
     * Issue a cryptographic challenge for a previously enrolled factor.
     * Maps to POST /v1/authfactors/{enrollment_id}/challenge.
     */
    async challengeFactor(req: ChallengeFactorRequest): Promise<ChallengeFactorResponse> {
        const resp = await fetch(
            `${this.baseUrl}/v1/authfactors/${encodeURIComponent(req.enrollmentId)}/challenge`,
            {
                method: 'POST',
                headers: { ...this.headers },
            }
        );

        if (!resp.ok) {
            const body = await resp.text();
            throw new AuthFactorError('ChallengeFactor', resp.status, body);
        }

        const raw = await resp.json() as RawChallenge;
        return {
            challengeId: raw.challenge_id,
            enrollmentId: raw.enrollment_id,
            nonce: raw.nonce,
            expiresAt: raw.expires_at,
            methodData: raw.method_data,
        };
    }

    /**
     * Verify the custodian's response to an outstanding challenge.
     * A successful call consumes the challenge (preventing replay).
     * Maps to POST /v1/authfactors/{challenge_id}/verify.
     */
    async verifyFactor(req: VerifyFactorRequest): Promise<void> {
        const resp = await fetch(
            `${this.baseUrl}/v1/authfactors/${encodeURIComponent(req.challengeId)}/verify`,
            {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', ...this.headers },
                body: JSON.stringify({
                    challenge_id: req.challengeId,
                    factor_name: req.factorName,
                    value: req.value,
                    metadata: req.metadata,
                }),
            }
        );

        if (!resp.ok) {
            const body = await resp.text();
            throw new AuthFactorError('VerifyFactor', resp.status, body);
        }
    }

    /**
     * List enrolled auth factors, optionally filtered by subject.
     * Maps to GET /v1/authfactors/list.
     */
    async listFactors(req: ListFactorsRequest = {}): Promise<ListFactorsResponse> {
        const params = new URLSearchParams();
        if (req.subject) {
            params.set('subject', req.subject);
        }

        const url = `${this.baseUrl}/v1/authfactors/list` +
            (params.size > 0 ? `?${params.toString()}` : '');

        const resp = await fetch(url, {
            method: 'GET',
            headers: { 'Accept': 'application/json', ...this.headers },
        });

        if (!resp.ok) {
            const body = await resp.text();
            throw new AuthFactorError('ListFactors', resp.status, body);
        }

        const raw = await resp.json() as { enrollments: RawEnrollment[]; total: number };
        return {
            enrollments: raw.enrollments.map(toEnrollment),
            total: raw.total,
        };
    }

    /**
     * Permanently revoke an enrollment.
     * Maps to DELETE /v1/authfactors/{enrollment_id}.
     */
    async revokeFactor(req: RevokeFactorRequest): Promise<void> {
        const resp = await fetch(
            `${this.baseUrl}/v1/authfactors/${encodeURIComponent(req.enrollmentId)}`,
            {
                method: 'DELETE',
                headers: { ...this.headers },
            }
        );

        if (!resp.ok) {
            const body = await resp.text();
            throw new AuthFactorError('RevokeFactor', resp.status, body);
        }
    }
}

// ---------------------------------------------------------------------------
// Internal wire types and helpers
// ---------------------------------------------------------------------------

interface RawEnrollment {
    enrollment_id: string;
    factor_name: string;
    user_id: string;
    tenant_id?: string;
    metadata?: Record<string, unknown>;
    enrolled_at: string;
    revoked_at?: string;
}

interface RawChallenge {
    challenge_id: string;
    enrollment_id: string;
    nonce: string;
    expires_at: string;
    method_data?: Record<string, unknown>;
}

function toEnrollment(raw: RawEnrollment): Enrollment {
    return {
        enrollmentId: raw.enrollment_id,
        factorName: raw.factor_name,
        userId: raw.user_id,
        tenantId: raw.tenant_id,
        metadata: raw.metadata,
        enrolledAt: raw.enrolled_at,
        revokedAt: raw.revoked_at,
    };
}

/** AuthFactorError is thrown when an auth factor operation fails. */
export class AuthFactorError extends Error {
    readonly method: string;
    readonly status: number;
    readonly body: string;

    constructor(method: string, status: number, body: string) {
        super(`authfactor ${method} failed: HTTP ${status}: ${body}`);
        this.name = 'AuthFactorError';
        this.method = method;
        this.status = status;
        this.body = body;
    }
}
