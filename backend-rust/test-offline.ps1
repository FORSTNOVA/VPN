param(
    [Parameter(Mandatory=$true)][string]$GoExe,
    [Parameter(Mandatory=$true)][string]$GoPath,
    [string]$Cache = (Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
    [string]$RustTargetDirectory = (Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference = 'Stop'
# Spawn this script in a child PowerShell process: process-only env never persists.
$root = Split-Path $PSScriptRoot -Parent
$backend = Join-Path $root 'backend'
$env:GOCACHE = $Cache
$env:GOPATH = $GoPath
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOFLAGS = '-mod=readonly -buildvcs=false'
$env:CGO_ENABLED = '0'
$env:CARGO_INCREMENTAL = '0'
$env:CARGO_TARGET_DIR = $RustTargetDirectory
$env:SMARTVPN_TEST_RUST_FIXTURES = Join-Path $PSScriptRoot 'tests\stun-fixtures.tsv'
function Invoke-Checked([string]$exe, [string[]]$arguments) {
    & $exe @arguments
    if ($LASTEXITCODE -ne 0) { throw "$exe failed ($LASTEXITCODE)" }
}
Push-Location $PSScriptRoot
try {
    Invoke-Checked 'cargo' @('fmt','--check')
    Invoke-Checked 'cargo' @('test','--offline','--locked')
    Invoke-Checked 'cargo' @('clippy','--offline','--locked','--all-targets','--','-D','warnings')
    Invoke-Checked 'cargo' @('build','--release','--offline','--locked')
} finally { Pop-Location }
# Deliberately explicit allowlist; never runs the full app/backend test suite.
$names = foreach ($file in @('native_bridge_contract','http_dispatch_independent_fixture','http_dispatch_independent_contract','http_dispatch_flow_safe','metadata_projection_safe','traffic_projection_safe','config_independent_legacy','traffic_projection_independent_legacy','metadata_independent_legacy','kernel_admission_independent_resources','embedded_kernel_independent_fixture','embedded_kernel_safe','kernel_admission_independent_legacy','kernel_admission_independent_fixture','kernel_admission_service_safe','kernel_admission_service_safe_windows','converter_independent_corpus','converter_service_safe','node_front_safe','converter_independent_legacy','profile_conversion_independent_legacy','adaptive_service_safe','adaptive_independent_health','adaptive_dns_independent_legacy','adaptive_independent_fixture','adaptive_independent_resources','android_resolver_independent_fixture','android_resolver_safe','bootstrap_service_safe','bootstrap_independent_fixture','bootstrap_independent_resources','privacy_log_independent_fixture','privacy_log_independent_resources','log_privacy_independent_legacy','privacy_log_safe','subscription_independent_legacy','subscription_http_independent_fixture','subscription_http_independent_contract','subscription_http_independent_resources','policy_dns_independent_fixture','subscription_http_service_safe','policy_dns_safe','app_control_fixture','app_control_contract','app_control_resource_contract','app_control_safe','driver_install_fixture','driver_install_contract','driver_install_http_contract','driver_install_safe_windows','driver_install_fault','wintun_independent_legacy_windows','speed_service_safe','speed_service_fixture','speed_service_contract','speed_independent_legacy','tun_report_fixture','tun_report_contract','tun_report_http_contract','tun_report_owned','tun_report_fault','converter','profile','nodes_uri','speedtest','stun','store','logscrub','migration_contract','migration_health_regression','store_probe','store_backend','health_service','settings_service','settings_service_http_contract','subscription_service','subscription_service_http_contract','profile_service','pipeline_file_contract','profile_flow_driver','quota_service','quota_http_contract','quota_flow_driver','connection_service','connection_http_contract','connection_flow_driver','kernel_service','kernel_adapter_contract','exit_service','kernel_exit_adapter_contract','region_service','region_service_http_contract','region_job_service','region_job_http_contract','manual_nodes','manual_service','manual_service_fixture','manual_service_contract','chain_service','chain_service_fixture','chain_service_contract','traffic_service','traffic_service_contract','diagnose_service','diagnose_service_fixture','diagnose_service_contract','chain_probe_service','probe_service_fixture','probe_service_contract','path_service','path_service_fixture','path_service_contract','path_monitor_service','path_monitor_service_fixture','path_monitor_contract','path_monitor_flow_guard','node_query_service','node_query_service_fixture','node_query_contract','lifecycle_service','lifecycle_service_fixture','lifecycle_service_contract','source_labels_service','source_labels_service_fixture','source_labels_service_contract','service_state','traffic_authority_contract','traffic_authority_fixture','settings_document','service_path_codec','path_authority_contract','path_authority_fixture','service_path','service_path_fault','service_health_codec','health_settings_closure','state_http_projection','service_health_test_helpers','health_authority_fixture_seed','region_authority_contract','region_authority_fixture','service_health_safe','control_authority_fixture_seed','control_authority_contract','service_control_codec','control_document_closure','control_authority_fixture','service_control_safe','catalog_authority_fixture_seed','catalog_authority_contract','catalog_authority_fixture','catalog_resource_closure','service_catalog_codec','service_catalog_safe','china_authority_fixture_seed','china_authority_contract','china_service_fixture','china_service_contract','china_integration_closure','service_china_codec','china_service_safe')) {
    Select-String -Path (Join-Path $backend ($file + '_test.go')) -Pattern '^func (Test\w+)\(' |
        ForEach-Object { $_.Matches[0].Groups[1].Value }
}
$names += @('TestChainOrderAndValidation','TestShouldTakeOver','TestRateFor','TestConnectionRowsReadTheKernelView','TestConnectionRowsAreCappedAndKeepTheHeaviest','TestConnectionProcessTrimsAPath','TestFlexStringReadsBothPortForms','TestTrafficWithoutAKernel','TestTrafficHistoryPersistsDeltasAcrossCalendarBoundaries','TestTrafficHistoryAtomicRetryAndFullPagination','TestTrafficHistoryAPIValidationAndOfflineRead')
$names += @('TestGeneratedConfig','TestGeneratedConfigPutsDomesticRulesBeforeTheProxy','TestGeneratedConfigReferencesNoListWhenThereIsNone','TestGeneratedConfigWithoutRules','TestGeneratedTUNConfigExcludesNodeDomains','TestGeneratedConfigProviders','TestGeneratedConfigCarriesOneMeasuringLoop')
$names += @('TestNextHealthStateSuccesses','TestNextHealthStateFailure','TestNextHealthStateCooldownHolds','TestNextHealthStateHonoursThreshold','TestHealthParamsNormalized','TestHealthParamsRoundTrip','TestCandidatesForRegion','TestCandidatesForRegionReadmitsAfterCooldown','TestCandidatesForRegionAreSorted','TestNodeHealthCooldownActive','TestDwellElapsed')
# Only explicitly audited pure/TempDir+fake-fetch China tests. Never installed-kernel probe.
$names += @('TestParseChinaIPListNormalises','TestParseChinaIPListRefusesWhatIsNotAList','TestEnsureChinaIPListFetchesAndCaches','TestEnsureChinaIPListDoesNotFetchWhatIsCurrent','TestEnsureChinaIPListRefreshesAnOverdueList','TestEnsureChinaIPListKeepsTheLastGoodList','TestEnsureChinaIPListWithNothingToFallBackOn','TestEnsureChinaIPListTreatsADamagedCacheAsMissing','TestRefreshChinaDirectAnswersAtOnceAndReportsAfterwards','TestStartChinaIPRefreshRefusesASecondRun')
$names += @('TestParseDNSFirstA','TestSOCKS5UDPEnvelopeRoundTrip','TestRelayEndpointStreamParity','TestRelayHandshakeTranscript')
$pattern = '^(' + ($names -join '|') + ')$'
Push-Location $backend
try {
    Write-Host "Default Go selected tests: $($names.Count)"
    Invoke-Checked $GoExe @('test','-count=1','-timeout=120s','-run',$pattern,'.')
    $bridgeNames = @('TestRustCoreBridgeParity','TestRustSubscriptionParity','TestRustNodeFieldsParity','TestRustNodeWireRejectsMalformed','TestRustExtendedNodeParity','TestRustExtendedInvalidUTF8Fallback','TestRustServiceLogicParity','TestRustLegacyNodeFieldsParity','TestRustLegacyFieldsBoundary','TestRustTrafficLogicParity','TestRustTrafficLogicBoundaries','TestRustHTTPParity','TestRustReachableParity','TestRustConfigParity','TestRustSettingsParity','TestRustHealthCoreParity')
    $bridgeNames += @('TestUDPReplyParserParity','TestUDPRequestEncoderParity','TestSTUNTransactionParity','TestRelayEndpointExport','TestNativeRelayHandshake','TestDNSRelaySelectionParity','TestSTUNRelaySelectionParity')
    $pattern = '^(' + (($names + $bridgeNames) -join '|') + ')$'
    $env:SMARTVPN_RUST_CORE_DLL = Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
    foreach ($key in @('SMARTVPN_TEST_EXPECT_RUST','SMARTVPN_TEST_EXPECT_SUBSCRIPTION_RUST','SMARTVPN_TEST_EXPECT_NODE_RUST','SMARTVPN_TEST_EXPECT_EXTENDED_NODE_RUST','SMARTVPN_TEST_EXPECT_SERVICE_RUST','SMARTVPN_TEST_EXPECT_LEGACY_NODE_RUST','SMARTVPN_TEST_EXPECT_TRAFFIC_RUST','SMARTVPN_TEST_EXPECT_HTTP_RUST','SMARTVPN_TEST_EXPECT_CONFIG_RUST','SMARTVPN_TEST_EXPECT_SETTINGS_RUST','SMARTVPN_TEST_EXPECT_HEALTH_RUST')) {
        [Environment]::SetEnvironmentVariable($key,'1','Process')
    }
    Invoke-Checked $GoExe @('test','-tags','rustcore','-count=1','-timeout=120s','-run',$pattern,'.')
} finally { Pop-Location }




# Missing-DLL compatibility pass in a fresh Go test process (loader uses sync.Once).
Push-Location $backend
try {
    $env:SMARTVPN_RUST_CORE_DLL = Join-Path $env:TEMP ('smartvpn-missing-' + [guid]::NewGuid().ToString() + '.dll')
    foreach ($key in @('SMARTVPN_TEST_EXPECT_RUST','SMARTVPN_TEST_EXPECT_SUBSCRIPTION_RUST','SMARTVPN_TEST_EXPECT_NODE_RUST','SMARTVPN_TEST_EXPECT_EXTENDED_NODE_RUST','SMARTVPN_TEST_EXPECT_SERVICE_RUST','SMARTVPN_TEST_EXPECT_LEGACY_NODE_RUST','SMARTVPN_TEST_EXPECT_TRAFFIC_RUST','SMARTVPN_TEST_EXPECT_HTTP_RUST','SMARTVPN_TEST_EXPECT_CONFIG_RUST','SMARTVPN_TEST_EXPECT_SETTINGS_RUST','SMARTVPN_TEST_EXPECT_HEALTH_RUST')) {
        [Environment]::SetEnvironmentVariable($key,'0','Process')
    }
    Invoke-Checked $GoExe @('test','-tags','rustcore','-count=1','-timeout=120s','-run',$pattern,'.')
} finally { Pop-Location }





















