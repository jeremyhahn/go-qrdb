# #1330 audit — pkg/api/mcp tools_coverage_new_test.go

Package unit-test time (`go test -tags "$(make -s print-build-tags)" -count=1 ./pkg/api/mcp/`):
- before: 43 s wall, `ok ... 10.465s`
- after: 23 s wall, `ok ... 10.438s` (145 tests removed or moved; 1 file of 610 lines reduced to shared mocks, renamed mock_admin_services_test.go)

| file | test | verdict | reason | covering test |
|---|---|---|---|---|
| tools_coverage_new_test.go | TestCovBarrierTools_Registration | removed | identical name list and order assertion | tools_barrier_test.go:TestBarrierTools_Registration |
| tools_coverage_new_test.go | TestCovHandleBarrierInitialize_Success | removed | same text assertion | tools_barrier_test.go:TestHandleBarrierInitialize_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierInitialize_MissingSecret | removed | same pass-through assertions | tools_barrier_test.go:TestHandleBarrierInitialize_MissingSecret |
| tools_coverage_new_test.go | TestCovHandleBarrierInitialize_ServiceError | removed | same ToolError + tool name | tools_barrier_test.go:TestHandleBarrierInitialize_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierUnseal_Success | removed | same text assertion | tools_barrier_test.go:TestHandleBarrierUnseal_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierUnseal_MissingSecret | removed | same IsError assertion | tools_barrier_test.go:TestHandleBarrierUnseal_MissingSecret |
| tools_coverage_new_test.go | TestCovHandleBarrierUnseal_ServiceError | removed | twin also asserts ToolError | tools_barrier_test.go:TestHandleBarrierUnseal_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierSeal_Success | removed | same text assertion | tools_barrier_test.go:TestHandleBarrierSeal_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierSeal_ServiceError | removed | same ToolError + tool name | tools_barrier_test.go:TestHandleBarrierSeal_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierStatus_Success | removed | twin asserts sealed plus strategy and initialized_at | tools_barrier_test.go:TestHandleBarrierStatus_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierStatus_ServiceError | removed | twin also asserts ToolError + tool name | tools_barrier_test.go:TestHandleBarrierStatus_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierShamirInitialize_Success | removed | twin asserts threshold, total and share_count | tools_barrier_test.go:TestHandleBarrierShamirInitialize_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierShamirInitialize_MissingThreshold | removed | same IsError assertion | tools_barrier_test.go:TestHandleBarrierShamirInitialize_MissingThreshold |
| tools_coverage_new_test.go | TestCovHandleBarrierShamirInitialize_MissingTotalShares | removed | same IsError assertion | tools_barrier_test.go:TestHandleBarrierShamirInitialize_MissingTotalShares |
| tools_coverage_new_test.go | TestCovHandleBarrierShamirInitialize_ServiceError | removed | twin also asserts ToolError | tools_barrier_test.go:TestHandleBarrierShamirInitialize_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryGenerate_Success | removed | twin asserts threshold, total and share_count | tools_barrier_test.go:TestHandleBarrierRecoveryGenerate_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryGenerate_MissingThreshold | removed | same IsError assertion | tools_barrier_test.go:TestHandleBarrierRecoveryGenerate_MissingThreshold |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryGenerate_ServiceError | removed | twin also asserts ToolError | tools_barrier_test.go:TestHandleBarrierRecoveryGenerate_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryDelete_Success | removed | same text assertion | tools_barrier_test.go:TestHandleBarrierRecoveryDelete_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryDelete_ServiceError | removed | twin also asserts ToolError + tool name | tools_barrier_test.go:TestHandleBarrierRecoveryDelete_ServiceError |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryStatus_True | removed | same has_recovery_keys=true assertion | tools_barrier_test.go:TestHandleBarrierRecoveryStatus_Success |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryStatus_False | removed | same has_recovery_keys=false assertion | tools_barrier_test.go:TestHandleBarrierRecoveryStatus_False |
| tools_coverage_new_test.go | TestCovHandleBarrierRecoveryStatus_ServiceError | removed | twin also asserts ToolError | tools_barrier_test.go:TestHandleBarrierRecoveryStatus_ServiceError |
| tools_coverage_new_test.go | TestCovWithLock | removed | asserts only NewServer non-nil (WithLock(nil)) | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations (lock tools missing from the registry fail it) |
| tools_coverage_new_test.go | TestCovWithEmbedding | removed | asserts only NewServer non-nil | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovWithRAG | removed | asserts only NewServer non-nil | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovWithUserService | removed | same field + qrdb_user_create assertion | server_options_test.go:TestServer_WithUserService |
| tools_coverage_new_test.go | TestCovWithRoleService | removed | same field + qrdb_role_create assertion | server_options_test.go:TestServer_WithRoleService |
| tools_coverage_new_test.go | TestCovWithCertProvider | removed | same field assertion (twin also checks tools) | server_options_test.go:TestServer_WithCertProvider |
| tools_coverage_new_test.go | TestCovWithBarrierService | removed | field + tool registration | tools_barrier_test.go:TestServer_WithBarrierService |
| tools_coverage_new_test.go | TestCovWithPINBackend | removed | asserts only NewServer non-nil | pin_backend_lockout_test.go:TestWithPINBackend_RawBackendIsThrottled |
| tools_coverage_new_test.go | TestCovWithCeremonyService | removed | asserts only NewServer non-nil | tools_ceremony_credentials_test.go:TestServer_WithCeremonyService |
| tools_coverage_new_test.go | TestCovWithCredentialService | removed | asserts only NewServer non-nil | tools_ceremony_credentials_test.go:TestServer_WithCredentialService |
| tools_coverage_new_test.go | TestCovWithCeremonyCA | removed | asserts only NewServer non-nil | tools_ceremony_credentials_test.go:TestServer_WithCeremonyService (asserts s.ceremonyCA) |
| tools_coverage_new_test.go | TestCovWithTenantService | removed | tool registration under WithTenantService | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovWithAuthFactorService | removed | tool registration under WithAuthFactorService | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovWithUserAuthFactorService | removed | tool registration under WithUserAuthFactorService | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovWithAuditLogger | removed | qrdb_audit_query registration under WithAuditLogger | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovWithKVService | moved | only check that WithKVService sets kvSvc | server_options_test.go:TestServer_WithKVService |
| tools_coverage_new_test.go | TestCovWithAdminService | moved | only check that WithAdminService sets adminSvc | server_options_test.go:TestServer_WithAdminService |
| tools_coverage_new_test.go | TestCovCeremonyCANotConfiguredError_Error | removed | same message assertion | tools_barrier_test.go:TestCeremonyCANotConfiguredError |
| tools_coverage_new_test.go | TestCovTenantTools_Registration | removed | asserts a tool count only | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations; tools_tenant_test.go:TestWithTenantService_ToolsRegistered |
| tools_coverage_new_test.go | TestCovHandleTenantCreate_Success | removed | twin asserts id and name | tools_tenant_test.go:TestHandleTenantCreate_Success |
| tools_coverage_new_test.go | TestCovHandleTenantCreate_MissingID | removed | same IsError assertion | tools_tenant_test.go:TestHandleTenantCreate_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantCreate_MissingName | removed | same IsError assertion | tools_tenant_test.go:TestHandleTenantCreate_EmptyName |
| tools_coverage_new_test.go | TestCovHandleTenantCreate_ServiceError | removed | same ErrTenantAlreadyExists, ToolError + tool name | tools_tenant_test.go:TestHandleTenantCreate_AlreadyExists |
| tools_coverage_new_test.go | TestCovHandleTenantGet_Success | removed | twin asserts id and name | tools_tenant_test.go:TestHandleTenantGet_Success |
| tools_coverage_new_test.go | TestCovHandleTenantGet_MissingID | removed | same IsError assertion | tools_tenant_test.go:TestHandleTenantGet_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantGet_ServiceError | removed | same ErrTenantNotFound; twin asserts ToolError | tools_tenant_test.go:TestHandleTenantGet_NotFound |
| tools_coverage_new_test.go | TestCovHandleTenantList_Success | removed | twin asserts count and first id | tools_tenant_test.go:TestHandleTenantList_Success |
| tools_coverage_new_test.go | TestCovHandleTenantList_ServiceError | removed | twin also asserts ToolError | tools_tenant_test.go:TestHandleTenantList_ServiceError |
| tools_coverage_new_test.go | TestCovHandleTenantUpdate_Success | removed | asserted only !IsError; twin asserts the updated name | tools_tenant_test.go:TestHandleTenantUpdate_Success |
| tools_coverage_new_test.go | TestCovHandleTenantUpdate_MissingID | removed | same IsError assertion | tools_tenant_test.go:TestHandleTenantUpdate_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantUpdate_ServiceError | removed | same ErrTenantNotFound; twin asserts ToolError | tools_tenant_test.go:TestHandleTenantUpdate_NotFound |
| tools_coverage_new_test.go | TestCovHandleTenantDelete_Success | removed | asserted only !IsError; twin asserts the id reaches DeleteTenant | tools_tenant_test.go:TestHandleTenantDelete_Success |
| tools_coverage_new_test.go | TestCovHandleTenantDelete_MissingID | removed | same IsError assertion | tools_tenant_test.go:TestHandleTenantDelete_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantDelete_ServiceError | removed | same ErrTenantNotFound; twin asserts ToolError | tools_tenant_test.go:TestHandleTenantDelete_NotFound |
| tools_coverage_new_test.go | TestCovHandleTenantUpdateQuotas_Success | removed | asserted only !IsError; twin asserts the id | tools_tenant_test.go:TestHandleTenantUpdateQuotas_Success |
| tools_coverage_new_test.go | TestCovHandleTenantUpdateQuotas_MissingID | removed | same IsError assertion | tools_tenant_test.go:TestHandleTenantUpdateQuotas_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantUpdateQuotas_ServiceError | removed | same ErrTenantNotFound; twin asserts ToolError | tools_tenant_test.go:TestHandleTenantUpdateQuotas_NotFound |
| tools_coverage_new_test.go | TestCovTenantBarrierTools_Registration | removed | asserts a tool count only | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierInit_Success | removed | asserted only !IsError; twin asserts arguments and payload | tools_tenant_barrier_test.go:TestHandleTenantBarrierInit_Success |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierInit_MissingID | removed | same IsError assertion | tools_tenant_barrier_test.go:TestHandleTenantBarrierInit_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierInit_ServiceError | removed | twin asserts ToolError + tool name | tools_tenant_barrier_test.go:TestHandleTenantBarrierInit_ServiceError |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierUnseal_SuccessWithKey | removed | twin asserts id, decoded key and unsealed=true | tools_tenant_barrier_test.go:TestHandleTenantBarrierUnseal_SuccessWithKey |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierUnseal_NoKey | removed | same no-key path | tools_tenant_barrier_test.go:TestHandleTenantBarrierUnseal_NoKeyDefaults |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierUnseal_MissingID | removed | same IsError assertion | tools_tenant_barrier_test.go:TestHandleTenantBarrierUnseal_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierUnseal_BadBase64 | removed | same IsError assertion | tools_tenant_barrier_test.go:TestHandleTenantBarrierUnseal_BadBase64 |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierUnseal_ServiceError | removed | twin asserts ToolError | tools_tenant_barrier_test.go:TestHandleTenantBarrierUnseal_ServiceError |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierSeal_Success | removed | asserted only !IsError; twin asserts id and payload | tools_tenant_barrier_test.go:TestHandleTenantBarrierSeal_Success |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierSeal_MissingID | removed | same IsError assertion | tools_tenant_barrier_test.go:TestHandleTenantBarrierSeal_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierSeal_ServiceError | removed | twin asserts ToolError | tools_tenant_barrier_test.go:TestHandleTenantBarrierSeal_ServiceError |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierStatus_Success | removed | asserted only !IsError; twin asserts the status payload | tools_tenant_barrier_test.go:TestHandleTenantBarrierStatus_Success |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierStatus_MissingID | removed | same IsError assertion | tools_tenant_barrier_test.go:TestHandleTenantBarrierStatus_EmptyID |
| tools_coverage_new_test.go | TestCovHandleTenantBarrierStatus_ServiceError | removed | twin asserts ToolError | tools_tenant_barrier_test.go:TestHandleTenantBarrierStatus_ServiceError |
| tools_coverage_new_test.go | TestCovAuthFactorTools_Registration | removed | asserts a tool count only | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovHandleAuthFactorEnroll_Success | removed | twin asserts the exact enrollment_id | authfactor_tool_test.go:TestHandleAuthFactorEnroll_Success |
| tools_coverage_new_test.go | TestCovHandleAuthFactorEnroll_MissingFactorName | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorEnroll_MissingFactorName |
| tools_coverage_new_test.go | TestCovHandleAuthFactorEnroll_MissingSubject | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorEnroll_MissingSubject |
| tools_coverage_new_test.go | TestCovHandleAuthFactorEnroll_BadMethodParams | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorEnroll_InvalidMethodParams |
| tools_coverage_new_test.go | TestCovHandleAuthFactorEnroll_ServiceError | removed | asserted only err != nil | authfactor_tool_test.go:TestHandleAuthFactorEnroll_ServiceError |
| tools_coverage_new_test.go | TestCovHandleAuthFactorChallenge_Success | removed | asserted only !IsError | authfactor_tool_test.go:TestHandleAuthFactorChallenge_Success |
| tools_coverage_new_test.go | TestCovHandleAuthFactorChallenge_MissingID | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorChallenge_MissingEnrollmentID |
| tools_coverage_new_test.go | TestCovHandleAuthFactorChallenge_ServiceError | removed | asserted only err != nil | authfactor_tool_test.go:TestHandleAuthFactorChallenge_ServiceError |
| tools_coverage_new_test.go | TestCovHandleAuthFactorVerify_Success | removed | asserted only !IsError | authfactor_tool_test.go:TestHandleAuthFactorVerify_Success |
| tools_coverage_new_test.go | TestCovHandleAuthFactorVerify_MissingChallengeID | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorVerify_MissingChallengeID |
| tools_coverage_new_test.go | TestCovHandleAuthFactorVerify_MissingResponseJSON | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorVerify_MissingResponseJSON |
| tools_coverage_new_test.go | TestCovHandleAuthFactorVerify_BadResponseJSON | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorVerify_InvalidResponseJSON |
| tools_coverage_new_test.go | TestCovHandleAuthFactorVerify_ServiceError | removed | asserted only err != nil | authfactor_tool_test.go:TestHandleAuthFactorVerify_ServiceError |
| tools_coverage_new_test.go | TestCovHandleAuthFactorList_Success | removed | twin asserts total=2 | authfactor_tool_test.go:TestHandleAuthFactorList_Success |
| tools_coverage_new_test.go | TestCovHandleAuthFactorList_WithRevokedAt | removed | asserted only !IsError; revoked_at output had no assertion anywhere, so the twin was tightened to assert revoked_at on the revoked entry and its absence on the active one | authfactor_tool_test.go:TestHandleAuthFactorList_Success (tightened) |
| tools_coverage_new_test.go | TestCovHandleAuthFactorList_ServiceError | removed | asserted only err != nil | authfactor_tool_test.go:TestHandleAuthFactorList_ServiceError |
| tools_coverage_new_test.go | TestCovHandleAuthFactorRevoke_Success | removed | asserted only !IsError | authfactor_tool_test.go:TestHandleAuthFactorRevoke_Success |
| tools_coverage_new_test.go | TestCovHandleAuthFactorRevoke_MissingID | removed | same IsError assertion | authfactor_tool_test.go:TestHandleAuthFactorRevoke_MissingEnrollmentID |
| tools_coverage_new_test.go | TestCovHandleAuthFactorRevoke_ServiceError | removed | asserted only err != nil | authfactor_tool_test.go:TestHandleAuthFactorRevoke_ServiceError |
| tools_coverage_new_test.go | TestCovUserAuthFactorTools_Registration | removed | asserts a tool count only | tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorAdd_Success | moved | only test of the add handler; tightened to assert user id, type, label, credential and metadata reach the service and the factor in the payload | user_auth_factor_tool_test.go:TestHandleUserAuthFactorAddTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorAdd_BadUserID | moved | only check; now also asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorAddTool_InvalidArgs/bad_user_id |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorAdd_MissingTypeOrCred | moved | only check; now also asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorAddTool_InvalidArgs/missing_type/cred |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorAdd_BadCredBase64 | moved | only check; now also asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorAddTool_InvalidArgs/bad_base64 |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorAdd_BadMetadataJSON | moved | only check; now also asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorAddTool_InvalidArgs/bad_metadata_JSON |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorAdd_ServiceError | moved | only check; tightened from err != nil to ToolError + tool name + wrapped cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorAddTool_ServiceError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorList_Success | moved | asserted only valid JSON; tightened to user id, total and factor ids | user_auth_factor_tool_test.go:TestHandleUserAuthFactorListTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorList_BadUserID | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorListTool_BadUserID |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorList_ServiceError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorListTool_ServiceError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorGet_Success | moved | asserted only !IsError; tightened to the factor id and type | user_auth_factor_tool_test.go:TestHandleUserAuthFactorGetTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorGet_MissingID | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorGetTool_MissingID |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorGet_ServiceError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorGetTool_ServiceError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRemove_Success | moved | asserted only !IsError; tightened to id and force reaching the service and status revoked | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRemoveTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRemove_MissingID | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRemoveTool_MissingID |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRemove_ServiceError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRemoveTool_ServiceError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRotate_Success | moved | asserted only !IsError; tightened to old id and decoded credential reaching the service and the new id in the payload | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRotateTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRotate_MissingFields | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRotateTool_InvalidArgs/missing_fields |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRotate_BadBase64 | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRotateTool_InvalidArgs/bad_base64 |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRotate_ServiceError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRotateTool_ServiceError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRelabel_Success | moved | asserted only !IsError; tightened to id and label reaching the service and the re-read factor in the payload | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRelabelTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRelabel_MissingFields | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRelabelTool_MissingFields |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRelabel_RelabelError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRelabelTool_RelabelError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorRelabel_GetError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorRelabelTool_GetError |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorLogin_Success | moved | asserted user_id non-nil; tightened to type and decoded key reaching the service and user_id 42 active | user_auth_factor_tool_test.go:TestHandleUserAuthFactorLoginTool_Success |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorLogin_MissingFields | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorLoginTool_InvalidArgs/missing_fields |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorLogin_BadBase64 | moved | only check; asserts the service is not called | user_auth_factor_tool_test.go:TestHandleUserAuthFactorLoginTool_InvalidArgs/bad_base64 |
| tools_coverage_new_test.go | TestCovHandleUserAuthFactorLogin_ServiceError | moved | tightened to ToolError + tool name + cause | user_auth_factor_tool_test.go:TestHandleUserAuthFactorLoginTool_ServiceError |
| tools_coverage_new_test.go | TestCovFactorToMCPMap_Nil | moved | only check of the nil case | user_auth_factor_tool_test.go:TestFactorToMCPMap_Nil |
| tools_coverage_new_test.go | TestCovFactorToMCPMap_WithOptionalFields | moved | asserted key presence only; tightened to formatted values, spki_credential, and absence when unset | user_auth_factor_tool_test.go:TestFactorToMCPMap_OptionalFields |
| tools_coverage_new_test.go | TestCovParseUint64_Valid | removed | tests strconv.ParseUint through a one-line wrapper | user_auth_factor_tool_test.go:TestHandleUserAuthFactorListTool_Success (asserts "42" reaches the service as 42) |
| tools_coverage_new_test.go | TestCovParseUint64_Invalid | removed | tests strconv.ParseUint through a one-line wrapper | user_auth_factor_tool_test.go:TestHandleUserAuthFactorListTool_BadUserID |
| tools_coverage_new_test.go | TestCovAuditTools_Registration | removed | tool names and count | audit_tool_test.go:TestWithAuditLogger_ToolsRegistered; tool_mapping_guard_test.go:TestToolMappingGuard_RegistryMatchesExpectations |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_Success | removed | same total assertion | audit_tool_test.go:TestHandleAuditQuery_Success |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_WithTimeRange | removed | asserted only !IsError; from/to reaching the filter had no assertion anywhere, so the twin was tightened to assert both | audit_tool_test.go:TestHandleAuditQuery_WithFromTo (tightened) |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_WithLimit | removed | asserted only !IsError; twin asserts limit 50 reaches the filter | audit_tool_test.go:TestHandleAuditQuery_WithFilterFields |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_BadFromTime | removed | same IsError assertion | audit_tool_test.go:TestHandleAuditQuery_InvalidFromTimestamp |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_BadToTime | removed | same IsError assertion | audit_tool_test.go:TestHandleAuditQuery_InvalidToTimestamp |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_BadLimit | removed | same IsError assertion | audit_tool_test.go:TestHandleAuditQuery_InvalidLimit |
| tools_coverage_new_test.go | TestCovHandleAuditQuery_ServiceError | removed | asserted only err != nil | audit_tool_test.go:TestHandleAuditQuery_ServiceError |
| tools_coverage_new_test.go | TestCovHandleUserGetByUsername_Success | moved + tightened | only test of the handler; asserted only !IsError. Now asserts tenant/username reach the service and the rendered record | tools_users_test.go:TestHandleUserGetByUsername |
| tools_coverage_new_test.go | TestCovHandleUserGetByUsername_MissingUsername | moved + tightened | only refusal test; now also asserts the service is not called | tools_users_test.go:TestHandleUserGetByUsername |
| tools_coverage_new_test.go | TestCovHandleUserGetByUsername_ServiceError | moved + tightened | asserted only err != nil; now asserts ToolError(qrdb_user_get_by_username) wrapping the cause | tools_users_test.go:TestHandleUserGetByUsername |
| tools_coverage_new_test.go | TestHandleUserGetByEmail | moved | already strong; moved to the handler's owner file | tools_users_test.go:TestHandleUserGetByEmail |
| tools_coverage_new_test.go | TestCovParseExactInt64_Int64 | moved | only int64 case; now a row (MaxInt64, lossless) in the owner table | tools_timeseries_test.go:TestParseExactInt64/int64 |
| tools_coverage_new_test.go | TestCovParseExactInt64_Int | moved | only int case; now a row (negative int) in the owner table | tools_timeseries_test.go:TestParseExactInt64/int |
| tools_coverage_new_test.go | TestCovParseExactInt64_Default | moved | only other-integer-type case; now a row in the owner table | tools_timeseries_test.go:TestParseExactInt64/other_integer_type |
| tools_coverage_new_test.go | TestCovHandleUserGetByCertThumbprint_MissingThumbprint | moved + tightened | only refusal test; now also asserts the service is not called | tools_users_test.go:TestHandleUserGetByCertThumbprint |
| tools_coverage_new_test.go | TestCovHandleUserGetByCertThumbprint_ServiceError | moved + tightened | asserted only err != nil; now asserts ToolError(qrdb_user_get_by_cert_thumbprint) wrapping the cause | tools_users_test.go:TestHandleUserGetByCertThumbprint |
| tools_coverage_new_test.go | TestCovHandleUserGetByCertThumbprint_Success | moved + tightened | only success test; asserted only !IsError. Now asserts the thumbprint reaches the service and the rendered record | tools_users_test.go:TestHandleUserGetByCertThumbprint |
