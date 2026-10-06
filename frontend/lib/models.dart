import 'package:flutter/material.dart';

import 'tokens.dart';

/// The two pseudo-entries the kernel offers next to the subscription nodes.
const autoGroup = 'SmartVPNAuto';
const fallbackGroup = 'SmartVPNFallback';

// ---------------------------------------------------------------------------
// Payload models. Field names mirror the JSON keys the local service sends.
// ---------------------------------------------------------------------------

class ProxyNode {
  const ProxyNode({
    required this.name,
    required this.type,
    required this.alive,
    required this.selected,
    required this.network,
    required this.tls,
    required this.wsHostConfigured,
    required this.wsPathConfigured,
    this.manual = false,
  });

  final String name;
  final String type;
  final bool alive;
  final bool selected;
  final String network;
  final bool tls;
  final bool wsHostConfigured;
  final bool wsPathConfigured;
  final bool manual;

  factory ProxyNode.fromJson(Map<String, dynamic> json) => ProxyNode(
        name: json['name'] as String? ?? '',
        type: json['type'] as String? ?? '',
        alive: json['alive'] as bool? ?? false,
        selected: json['selected'] as bool? ?? false,
        network: json['network'] as String? ?? '',
        tls: json['tls'] as bool? ?? false,
        wsHostConfigured: json['wsHostConfigured'] as bool? ?? false,
        wsPathConfigured: json['wsPathConfigured'] as bool? ?? false,
        // True for a node the user pasted rather than one the subscription lists.
        manual: json['manual'] as bool? ?? false,
      );
}

class SiteCheck {
  const SiteCheck(
      {required this.name,
      required this.state,
      required this.detail,
      required this.httpCode,
      required this.latencyMs});

  final String name;
  final String state;
  final String detail;
  final int httpCode;
  final int latencyMs;

  factory SiteCheck.fromJson(Map<String, dynamic> value) => SiteCheck(
        name: value['name'] as String? ?? '',
        state: value['state'] as String? ?? 'error',
        detail: value['detail'] as String? ?? '',
        httpCode: value['httpCode'] as int? ?? 0,
        latencyMs: value['latencyMs'] as int? ?? 0,
      );
}

class RegionInfo {
  const RegionInfo({
    required this.name,
    required this.country,
    required this.exitIp,
    required this.status,
    required this.detail,
    required this.sources,
    required this.verifiedAt,
    required this.stale,
  });

  final String name;
  final String country;
  final String exitIp;
  final String status;
  final String detail;
  final List<String> sources;
  final String verifiedAt;
  final bool stale;

  /// Only a fresh, cross-checked region may be used for the locked pool.
  bool get usable => status == 'verified' && !stale;

  factory RegionInfo.fromJson(Map<String, dynamic> value) => RegionInfo(
        name: value['name'] as String? ?? '',
        country: value['country'] as String? ?? '',
        exitIp: value['exitIp'] as String? ?? '',
        status: value['status'] as String? ?? 'unverified',
        detail: value['detail'] as String? ?? '',
        sources: (value['sources'] as List<dynamic>? ?? const [])
            .whereType<String>()
            .toList(growable: false),
        verifiedAt: value['verifiedAt'] as String? ?? '',
        stale: value['stale'] as bool? ?? false,
      );
}

class RegionJob {
  const RegionJob({
    required this.running,
    required this.total,
    required this.done,
    required this.verified,
    required this.skipped,
    required this.failed,
    required this.lastError,
  });

  final bool running;
  final int total;
  final int done;
  final int verified;
  final int skipped;
  final int failed;
  final String lastError;

  factory RegionJob.fromJson(Map<String, dynamic> value) => RegionJob(
        running: value['running'] as bool? ?? false,
        total: value['total'] as int? ?? 0,
        done: value['done'] as int? ?? 0,
        verified: value['verified'] as int? ?? 0,
        skipped: value['skipped'] as int? ?? 0,
        failed: value['failed'] as int? ?? 0,
        lastError: value['lastError'] as String? ?? '',
      );
}

class HealthParams {
  const HealthParams({
    required this.connectTimeoutMs,
    required this.patrolIntervalSec,
    required this.failureThreshold,
    required this.cooldownSec,
    required this.minDwellSec,
    required this.recoverySuccesses,
  });

  final int connectTimeoutMs;
  final int patrolIntervalSec;
  final int failureThreshold;
  final int cooldownSec;
  final int minDwellSec;
  final int recoverySuccesses;

  factory HealthParams.fromJson(Map<String, dynamic> value) => HealthParams(
        connectTimeoutMs: value['connectTimeoutMs'] as int? ?? 3000,
        patrolIntervalSec: value['patrolIntervalSec'] as int? ?? 60,
        failureThreshold: value['failureThreshold'] as int? ?? 2,
        cooldownSec: value['cooldownSec'] as int? ?? 60,
        minDwellSec: value['minDwellSec'] as int? ?? 30,
        recoverySuccesses: value['recoverySuccesses'] as int? ?? 3,
      );

  Map<String, dynamic> toJson() => {
        'connectTimeoutMs': connectTimeoutMs,
        'patrolIntervalSec': patrolIntervalSec,
        'failureThreshold': failureThreshold,
        'cooldownSec': cooldownSec,
        'minDwellSec': minDwellSec,
        'recoverySuccesses': recoverySuccesses,
      };
}

class HealthSummary {
  const HealthSummary({
    required this.counts,
    required this.poolSize,
    required this.lockedRegion,
    required this.params,
    required this.sweepNote,
  });

  final Map<String, int> counts;
  final int poolSize;
  final String lockedRegion;
  final HealthParams params;

  /// Set when a sweep condemned every node of the locked region while a real
  /// request still got through: a measurement that says the region is dead
  /// while traffic flows. It clears itself once a sweep measures something.
  final String sweepNote;

  int count(String state) => counts[state] ?? 0;

  factory HealthSummary.fromJson(Map<String, dynamic> value) {
    final raw = value['counts'] as Map<String, dynamic>? ?? const {};
    return HealthSummary(
      counts: raw.map((key, item) => MapEntry(key, item as int? ?? 0)),
      poolSize: value['poolSize'] as int? ?? 0,
      lockedRegion: value['lockedRegion'] as String? ?? '',
      params: HealthParams.fromJson(
          value['params'] as Map<String, dynamic>? ?? const {}),
      sweepNote: value['sweepNote'] as String? ?? '',
    );
  }
}

class NodeHealth {
  const NodeHealth({
    required this.state,
    required this.latencyMs,
    required this.consecutiveFailures,
    required this.cooldownUntil,
  });

  final String state;
  final int latencyMs;
  final int consecutiveFailures;
  final String cooldownUntil;

  factory NodeHealth.fromJson(Map<String, dynamic> value) => NodeHealth(
        state: value['state'] as String? ?? '',
        latencyMs: value['latencyMs'] as int? ?? 0,
        consecutiveFailures: value['consecutiveFailures'] as int? ?? 0,
        cooldownUntil: value['cooldownUntil'] as String? ?? '',
      );
}

class SwitchEvent {
  const SwitchEvent({
    required this.at,
    required this.fromNode,
    required this.toNode,
    required this.trigger,
    required this.evidence,
  });

  final String at;
  final String fromNode;
  final String toNode;
  final String trigger;
  final String evidence;

  factory SwitchEvent.fromJson(Map<String, dynamic> value) => SwitchEvent(
        at: value['at'] as String? ?? '',
        fromNode: value['fromNode'] as String? ?? '',
        toNode: value['toNode'] as String? ?? '',
        trigger: value['trigger'] as String? ?? '',
        evidence: value['evidence'] as String? ?? '',
      );
}

class TunCheck {
  const TunCheck({
    required this.name,
    required this.state,
    required this.detail,
    required this.value,
  });

  final String name;
  final String state;
  final String detail;
  final String value;

  factory TunCheck.fromJson(Map<String, dynamic> value) => TunCheck(
        name: value['name'] as String? ?? '',
        state: value['state'] as String? ?? 'warn',
        detail: value['detail'] as String? ?? '',
        value: value['value'] as String? ?? '',
      );
}

class UdpRelay {
  const UdpRelay({
    required this.target,
    required this.reachable,
    required this.latencyMs,
    required this.answer,
    required this.reason,
  });

  final String target;
  final bool reachable;
  final int latencyMs;
  final String answer;
  final String reason;

  factory UdpRelay.fromJson(Map<String, dynamic> value) => UdpRelay(
        target: value['target'] as String? ?? '',
        reachable: value['reachable'] as bool? ?? false,
        latencyMs: value['latencyMs'] as int? ?? 0,
        answer: value['answer'] as String? ?? '',
        reason: value['reason'] as String? ?? '',
      );
}

/// What the kernel says it is carrying: totals for its lifetime, the rate over
/// the last two samples, and the connections behind them.
class TrafficSnapshot {
  const TrafficSnapshot({
    required this.available,
    required this.reason,
    required this.uploadTotal,
    required this.downloadTotal,
    required this.uploadRate,
    required this.downloadRate,
    required this.connectionCount,
    required this.shownCount,
    required this.connections,
  });

  const TrafficSnapshot.idle()
      : available = false,
        reason = '',
        uploadTotal = 0,
        downloadTotal = 0,
        uploadRate = 0,
        downloadRate = 0,
        connectionCount = 0,
        shownCount = 0,
        connections = const [];

  const TrafficSnapshot.unavailable(this.reason)
      : available = false,
        uploadTotal = 0,
        downloadTotal = 0,
        uploadRate = 0,
        downloadRate = 0,
        connectionCount = 0,
        shownCount = 0,
        connections = const [];

  final bool available;
  final String reason;
  final int uploadTotal;
  final int downloadTotal;
  final int uploadRate;
  final int downloadRate;
  final int connectionCount;
  final int shownCount;
  final List<ConnectionRow> connections;

  factory TrafficSnapshot.fromJson(Map<String, dynamic> value) =>
      TrafficSnapshot(
        available: value['available'] as bool? ?? false,
        reason: value['reason'] as String? ?? '',
        uploadTotal: value['uploadTotal'] as int? ?? 0,
        downloadTotal: value['downloadTotal'] as int? ?? 0,
        uploadRate: value['uploadRate'] as int? ?? 0,
        downloadRate: value['downloadRate'] as int? ?? 0,
        connectionCount: value['connectionCount'] as int? ?? 0,
        shownCount: value['shownCount'] as int? ?? 0,
        connections: (value['connections'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(ConnectionRow.fromJson)
            .toList(growable: false),
      );
}

class ConnectionRow {
  const ConnectionRow({
    required this.process,
    required this.target,
    required this.network,
    required this.type,
    required this.rule,
    required this.chain,
    required this.upload,
    required this.download,
    required this.durationMs,
  });

  final String process;
  final String target;
  final String network;
  final String type;
  final String rule;
  final String chain;
  final int upload;
  final int download;
  final int durationMs;

  factory ConnectionRow.fromJson(Map<String, dynamic> value) => ConnectionRow(
        process: value['process'] as String? ?? '',
        target: value['target'] as String? ?? '',
        network: value['network'] as String? ?? '',
        type: value['type'] as String? ?? '',
        rule: value['rule'] as String? ?? '',
        chain: value['chain'] as String? ?? '',
        upload: value['upload'] as int? ?? 0,
        download: value['download'] as int? ?? 0,
        durationMs: value['durationMs'] as int? ?? 0,
      );
}

/// Traffic and expiry details returned in a subscription's headers.
class SubscriptionUserInfo {
  const SubscriptionUserInfo({
    required this.upload,
    required this.download,
    required this.total,
    required this.expire,
  });

  final int upload;
  final int download;
  final int total;
  final int expire;

  int get used => upload + download;
  double get progress => total > 0 ? (used / total).clamp(0.0, 1.0) : 0.0;

  static String _formatBytes(int bytes) {
    if (bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    var value = bytes.toDouble();
    var idx = 0;
    while (value >= 1024.0 && idx < units.length - 1) {
      value /= 1024.0;
      idx++;
    }
    return '${value.toStringAsFixed(idx == 0 ? 0 : 1)} ${units[idx]}';
  }

  String get usedString => _formatBytes(used);
  String get totalString => _formatBytes(total);

  String get expireString {
    if (expire <= 0) return '长期有效';
    final dt = DateTime.fromMillisecondsSinceEpoch(expire * 1000);
    return '${dt.year}-${dt.month.toString().padLeft(2, '0')}-${dt.day.toString().padLeft(2, '0')}';
  }

  factory SubscriptionUserInfo.fromJson(Map<String, dynamic> value) =>
      SubscriptionUserInfo(
        upload: (value['upload'] as num?)?.toInt() ?? 0,
        download: (value['download'] as num?)?.toInt() ?? 0,
        total: (value['total'] as num?)?.toInt() ?? 0,
        expire: (value['expire'] as num?)?.toInt() ?? 0,
      );
}

/// One saved subscription.
class SavedSubscription {
  const SavedSubscription({
    required this.id,
    required this.url,
    required this.label,
    this.prefix = '',
    this.enabled = true,
    required this.updatedAt,
    required this.nodeCount,
    required this.lastError,
    this.userInfo,
    this.quotaUpdatedAt = 0,
    this.quotaError = '',
  });

  final String id;
  final String url;
  final String label;
  final String prefix;
  final bool enabled;
  final int updatedAt;
  final int nodeCount;
  final String lastError;
  final SubscriptionUserInfo? userInfo;
  final int quotaUpdatedAt;
  final String quotaError;

  bool get hasFailed => lastError.isNotEmpty;

  factory SavedSubscription.fromJson(Map<String, dynamic> value) =>
      SavedSubscription(
        id: value['id'] as String? ?? '',
        url: value['url'] as String? ?? '',
        label: value['label'] as String? ?? '',
        prefix: value['prefix'] as String? ?? '',
        enabled: value['enabled'] as bool? ?? true,
        updatedAt: value['updatedAt'] as int? ?? 0,
        nodeCount: value['nodeCount'] as int? ?? 0,
        lastError: value['lastError'] as String? ?? '',
        userInfo: value['userInfo'] is Map<String, dynamic>
            ? SubscriptionUserInfo.fromJson(
                value['userInfo'] as Map<String, dynamic>)
            : null,
        quotaUpdatedAt: (value['quotaUpdatedAt'] as num?)?.toInt() ?? 0,
        quotaError: value['quotaError'] as String? ?? '',
      );
}

/// A node the user pasted, described by the service that parsed it.
class ManualNode {
  const ManualNode({
    required this.name,
    required this.type,
    required this.server,
    required this.port,
  });

  final String name;
  final String type;
  final String server;
  final int port;

  String get endpoint => '$server:$port';

  factory ManualNode.fromJson(Map<String, dynamic> value) => ManualNode(
        name: value['name'] as String? ?? '',
        type: value['type'] as String? ?? '',
        server: value['server'] as String? ?? '',
        port: value['port'] as int? ?? 0,
      );
}

/// How long ago something happened, in words. Unix seconds in.
String updatedLabel(int unixSeconds) {
  if (unixSeconds <= 0) {
    return '尚未拉取';
  }
  final at = DateTime.fromMillisecondsSinceEpoch(unixSeconds * 1000);
  final elapsed = DateTime.now().difference(at);
  if (elapsed.inMinutes < 1) {
    return '刚刚';
  }
  if (elapsed.inMinutes < 60) {
    return '${elapsed.inMinutes} 分钟前';
  }
  if (elapsed.inHours < 24) {
    return '${elapsed.inHours} 小时前';
  }
  return '${elapsed.inDays} 天前';
}

String subscriptionDetail(SavedSubscription entry) => entry.hasFailed
    ? '上次拉取失败：${entry.lastError}'
    : '${entry.nodeCount} 个节点 · 更新于 ${updatedLabel(entry.updatedAt)}';

/// What the paste dialog says about a link it has just parsed.
String parsedNodeLabel(ManualNode node) =>
    '${node.name}　${node.type}　${node.endpoint}';

/// Where the service keeps its files, and whether this copy keeps them beside
/// itself instead of in the user's profile.
String profileLabel({required String home, required bool portable}) {
  if (home.isEmpty) return '—';
  return portable ? '$home（便携：数据都在本文件夹内）' : home;
}

/// What a geolocation service saw when the tunnel asked it: the exit address and
/// who operates it.
class EgressFacts {
  const EgressFacts({
    required this.ipv4,
    required this.ipv6,
    required this.country,
    required this.countryCode,
    required this.city,
    required this.asn,
    required this.isp,
    required this.org,
    required this.error,
  });

  const EgressFacts.empty()
      : ipv4 = '',
        ipv6 = '',
        country = '',
        countryCode = '',
        city = '',
        asn = '',
        isp = '',
        org = '',
        error = '';

  final String ipv4;
  final String ipv6;
  final String country;
  final String countryCode;
  final String city;
  final String asn;
  final String isp;
  final String org;
  final String error;

  factory EgressFacts.fromJson(Map<String, dynamic> value) => EgressFacts(
        ipv4: value['ipv4'] as String? ?? '',
        ipv6: value['ipv6'] as String? ?? '',
        country: value['country'] as String? ?? '',
        countryCode: value['countryCode'] as String? ?? '',
        city: value['city'] as String? ?? '',
        asn: value['asn'] as String? ?? '',
        isp: value['isp'] as String? ?? '',
        org: value['org'] as String? ?? '',
        error: value['error'] as String? ?? '',
      );
}

/// Which resolver answered for the name we just looked up, and whether it
/// belongs to the same country as the exit address.
class DnsFacts {
  const DnsFacts({
    required this.resolver,
    required this.geo,
    required this.country,
    required this.matchesExit,
    required this.error,
  });

  const DnsFacts.empty()
      : resolver = '',
        geo = '',
        country = '',
        matchesExit = false,
        error = '';

  final String resolver;
  final String geo;
  final String country;
  final bool matchesExit;
  final String error;

  factory DnsFacts.fromJson(Map<String, dynamic> value) => DnsFacts(
        resolver: value['resolver'] as String? ?? '',
        geo: value['geo'] as String? ?? '',
        country: value['country'] as String? ?? '',
        matchesExit: value['matchesExit'] as bool? ?? false,
        error: value['error'] as String? ?? '',
      );
}

/// Whether the node forwards datagrams, and whether the address a STUN server
/// saw is the same one the tunnel presents over TCP.
class UdpFacts {
  const UdpFacts({
    required this.relay,
    required this.mapped,
    required this.matchesExit,
    required this.error,
  });

  const UdpFacts.empty()
      : relay = null,
        mapped = '',
        matchesExit = false,
        error = '';

  final UdpRelay? relay;
  final String mapped;
  final bool matchesExit;
  final String error;

  factory UdpFacts.fromJson(Map<String, dynamic> value) => UdpFacts(
        relay: value['relay'] is Map<String, dynamic>
            ? UdpRelay.fromJson(value['relay'] as Map<String, dynamic>)
            : null,
        mapped: value['mapped'] as String? ?? '',
        matchesExit: value['matchesExit'] as bool? ?? false,
        error: value['error'] as String? ?? '',
      );
}

/// IP quality, fraud scoring, and leak status inspired by IPCheck.ing / MyIP.
class IpQualityFacts {
  const IpQualityFacts({
    required this.ipType,
    required this.fraudScore,
    required this.riskLevel,
    required this.isProxy,
    required this.isVpn,
    required this.isTor,
    required this.isHosting,
    required this.isNative,
    required this.webrtcLeak,
    required this.dnsLeak,
  });

  const IpQualityFacts.empty()
      : ipType = '',
        fraudScore = 0,
        riskLevel = '',
        isProxy = false,
        isVpn = false,
        isTor = false,
        isHosting = false,
        isNative = false,
        webrtcLeak = false,
        dnsLeak = false;

  final String ipType;
  final int fraudScore;
  final String riskLevel;
  final bool isProxy;
  final bool isVpn;
  final bool isTor;
  final bool isHosting;
  final bool isNative;
  final bool webrtcLeak;
  final bool dnsLeak;

  factory IpQualityFacts.fromJson(Map<String, dynamic> value) => IpQualityFacts(
        ipType: value['ipType'] as String? ?? '',
        fraudScore: value['fraudScore'] as int? ?? 0,
        riskLevel: value['riskLevel'] as String? ?? '',
        isProxy: value['isProxy'] as bool? ?? false,
        isVpn: value['isVpn'] as bool? ?? false,
        isTor: value['isTor'] as bool? ?? false,
        isHosting: value['isHosting'] as bool? ?? false,
        isNative: value['isNative'] as bool? ?? false,
        webrtcLeak: value['webrtcLeak'] as bool? ?? false,
        dnsLeak: value['dnsLeak'] as bool? ?? false,
      );
}

/// Everything the check page shows in one pass.
class DiagnoseReport {
  const DiagnoseReport({
    required this.node,
    required this.temporary,
    required this.egress,
    required this.dns,
    required this.udp,
    this.quality = const IpQualityFacts.empty(),
    required this.sites,
  });

  final String node;
  final bool temporary;
  final EgressFacts egress;
  final DnsFacts dns;
  final UdpFacts udp;
  final IpQualityFacts quality;
  final List<SiteCheck> sites;

  factory DiagnoseReport.fromJson(Map<String, dynamic> value) => DiagnoseReport(
        node: value['node'] as String? ?? '',
        temporary: value['temporary'] as bool? ?? false,
        egress: EgressFacts.fromJson(
            value['egress'] as Map<String, dynamic>? ?? const {}),
        dns: DnsFacts.fromJson(value['dns'] as Map<String, dynamic>? ?? const {}),
        udp: UdpFacts.fromJson(value['udp'] as Map<String, dynamic>? ?? const {}),
        quality: IpQualityFacts.fromJson(
            value['quality'] as Map<String, dynamic>? ?? const {}),
        sites: (value['sites'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(SiteCheck.fromJson)
            .toList(growable: false),
      );
}

class SpeedTestResult {
  const SpeedTestResult({
    required this.target,
    required this.megaBytes,
    required this.downMbps,
    required this.upMbps,
    required this.downBytes,
    required this.upBytes,
    required this.downMs,
    required this.upMs,
    required this.error,
  });

  final String target;
  final int megaBytes;
  final double downMbps;
  final double upMbps;
  final int downBytes;
  final int upBytes;
  final int downMs;
  final int upMs;
  final String error;

  factory SpeedTestResult.fromJson(Map<String, dynamic> value) =>
      SpeedTestResult(
        target: value['target'] as String? ?? '',
        megaBytes: value['megaBytes'] as int? ?? 0,
        downMbps: (value['downMbps'] as num?)?.toDouble() ?? 0,
        upMbps: (value['upMbps'] as num?)?.toDouble() ?? 0,
        downBytes: value['downBytes'] as int? ?? 0,
        upBytes: value['upBytes'] as int? ?? 0,
        downMs: value['downMs'] as int? ?? 0,
        upMs: value['upMs'] as int? ?? 0,
        error: value['error'] as String? ?? '',
      );
}

/// What the exit rows say. The verdicts are the service's; this only puts them
/// into words.
String egressAddressLabel({required String address, required String fallback}) =>
    address.isEmpty ? (fallback.isEmpty ? '未取得' : fallback) : address;

/// Where the exit address is, and who runs it. The two are separate rows on the
/// page and one line in the copied report.
String egressLocationLabel(EgressFacts egress) {
  final parts = [
    if (egress.city.isNotEmpty) egress.city,
    if (egress.country.isNotEmpty) egress.country,
  ];
  return parts.isEmpty ? '—' : parts.join(' · ');
}

String egressOperatorLabel(EgressFacts egress) {
  final parts = [
    if (egress.asn.isNotEmpty) egress.asn,
    if (egress.isp.isNotEmpty) egress.isp,
    if (egress.org.isNotEmpty && egress.org != egress.isp) egress.org,
  ];
  return parts.isEmpty ? '—' : parts.join(' · ');
}

/// The DNS verdict says where names are resolved, not that something is broken:
/// resolving at the exit is the tunneled answer, resolving locally is what the
/// system proxy leaves in place.
String dnsVerdictLabel(DnsFacts dns) {
  if (dns.error.isNotEmpty) return '未测出';
  if (dns.resolver.isEmpty) return '未测出';
  return dns.matchesExit ? '在出口所在地解析' : '在本地解析（未走出口）';
}

String dnsVerdictBadge(DnsFacts dns) {
  if (dns.error.isNotEmpty || dns.resolver.isEmpty) return '未测出';
  return dns.matchesExit ? '与出口同地' : '本地解析';
}

String udpVerdictLabel(UdpFacts udp) {
  if (udp.error.isNotEmpty && udp.mapped.isEmpty) return '未测出';
  return udp.matchesExit ? '与 TCP 出口一致' : '与 TCP 出口不一致';
}

String qualityRiskBadge(IpQualityFacts quality) {
  if (quality.riskLevel.isEmpty) return '未知';
  return quality.riskLevel;
}

Color qualityRiskColor(IpQualityFacts quality) {
  if (quality.fraudScore > 60) return Tokens.bad;
  if (quality.fraudScore > 25) return Tokens.warn;
  return Tokens.ok;
}

String webrtcVerdictLabel(UdpFacts udp, IpQualityFacts quality) {
  if (udp.error.isNotEmpty && udp.mapped.isEmpty) return '未测出';
  if (quality.webrtcLeak) return '存在 WebRTC 泄露（STUN 与 TCP 出口不一致）';
  return '安全（STUN 与 TCP 出口一致）';
}

String webrtcVerdictBadge(UdpFacts udp, IpQualityFacts quality) {
  if (udp.error.isNotEmpty && udp.mapped.isEmpty) return '未测出';
  return quality.webrtcLeak ? '存在泄露' : '安全无泄露';
}

String speedLabel(double mbps) =>
    mbps <= 0 ? '—' : '${mbps.toStringAsFixed(1)} Mbit/s';

String speedDetailLabel(SpeedTestResult speed) {
  if (speed.downBytes <= 0 && speed.upBytes <= 0) return '—';
  final seconds = ((speed.downMs + speed.upMs) / 1000).toStringAsFixed(1);
  return '实测 ${byteLabel(speed.downBytes)} ↓ / ${byteLabel(speed.upBytes)} ↑ · $seconds 秒';
}

/// The report the copy button puts on the clipboard: the same rows the page
/// shows, in a form that can be pasted into a message when asking for help.
String diagnoseReportText(DiagnoseReport report,
    {SpeedTestResult? speed, DateTime? at}) {
  final lines = <String>[
    'SmartVPN 检测报告（整合 IPCheck.ing 工具箱标准）',
    '节点：${report.node.isEmpty ? '未知' : report.node}'
        '${report.temporary ? '（临时启动内核）' : ''}',
    '出口 IPv4：${egressAddressLabel(address: report.egress.ipv4, fallback: report.egress.error)}',
    '出口 IPv6：${report.egress.ipv6.isEmpty ? '未取得' : report.egress.ipv6}',
    '出口地区：${egressLocationLabel(report.egress)}　${egressOperatorLabel(report.egress)}',
    if (report.quality.ipType.isNotEmpty) ...[
      'IP 类型：${report.quality.ipType} · 欺诈分：${report.quality.fraudScore}/100（${report.quality.riskLevel}）',
      'WebRTC 泄露：${report.quality.webrtcLeak ? '存在泄露风险' : '安全无泄露'}',
      'DNS 泄露：${report.quality.dnsLeak ? '存在本地/跨国泄露' : '安全无泄露'}',
    ],
    'DNS 解析出口：${report.dns.resolver.isEmpty ? report.dns.error : '${report.dns.resolver}（${report.dns.geo.isEmpty ? '归属未知' : report.dns.geo}）'}'
        '　${dnsVerdictLabel(report.dns)}',
    'UDP 中继：${report.udp.relay == null ? '未测出' : (report.udp.relay!.reachable ? '可用 ${report.udp.relay!.latencyMs} ms' : (report.udp.relay!.reason.isEmpty ? '不可用' : report.udp.relay!.reason))}',
    'UDP 出口：${report.udp.mapped.isEmpty ? report.udp.error : report.udp.mapped}'
        '　${udpVerdictLabel(report.udp)}',
    '网站：${report.sites.map((site) => '${site.name} ${siteStateLabel(site.state)}${site.latencyMs > 0 ? ' ${site.latencyMs}ms' : ''}').join(' · ')}',
    if (speed != null && speed.error.isEmpty)
      '网速：下行 ${speedLabel(speed.downMbps)} / 上行 ${speedLabel(speed.upMbps)}'
          '（${speedDetailLabel(speed)}，目标 ${speed.target}）',
    if (speed != null && speed.error.isNotEmpty) '网速：${speed.error}',
    '检查时间：${(at ?? DateTime.now()).toString().split('.').first}',
  ];
  return lines.join('\n');
}

/// One site the path monitor checks, through the tunnel, on a timer.
class PathTarget {
  const PathTarget({
    required this.label,
    required this.url,
    required this.builtin,
    required this.enabled,
  });

  final String label;
  final String url;
  final bool builtin;
  final bool enabled;

  factory PathTarget.fromJson(Map<String, dynamic> value) => PathTarget(
        label: value['label'] as String? ?? '',
        url: value['url'] as String? ?? '',
        builtin: value['builtin'] as bool? ?? false,
        enabled: value['enabled'] as bool? ?? false,
      );

  // Whether a target is built in is the service's judgement, and it makes it
  // from the address, so the page does not send it back.
  Map<String, dynamic> toJson() =>
      {'label': label, 'url': url, 'enabled': enabled};

  PathTarget withEnabled(bool value) =>
      PathTarget(label: label, url: url, builtin: builtin, enabled: value);
}

class PathChecks {
  const PathChecks({
    required this.enabled,
    required this.intervalSec,
    required this.targets,
  });

  const PathChecks.empty()
      : enabled = false,
        intervalSec = 120,
        targets = const [];

  final bool enabled;
  final int intervalSec;
  final List<PathTarget> targets;

  List<PathTarget> get enabledTargets =>
      targets.where((target) => target.enabled).toList(growable: false);

  factory PathChecks.fromJson(Map<String, dynamic> value) => PathChecks(
        enabled: value['enabled'] as bool? ?? false,
        intervalSec: value['intervalSec'] as int? ?? 120,
        targets: (value['targets'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(PathTarget.fromJson)
            .toList(growable: false),
      );

  Map<String, dynamic> toJson() => {
        'enabled': enabled,
        'intervalSec': intervalSec,
        'targets': [for (final target in targets) target.toJson()],
      };

  PathChecks withTargets(List<PathTarget> value) =>
      PathChecks(enabled: enabled, intervalSec: intervalSec, targets: value);
}

/// What the monitor's last round found, and what it did about it.
class PathStatus {
  const PathStatus({
    required this.at,
    required this.running,
    required this.activeNode,
    required this.results,
    required this.failures,
    required this.switched,
    required this.note,
    this.alertId = 0,
    this.alert = '',
    this.retryAt = '',
    this.retryMode = '',
    this.demanded = false,
    this.phase = '',
  });

  const PathStatus.empty()
      : at = '',
        running = false,
        activeNode = '',
        results = const [],
        failures = const [],
        switched = const [],
        note = '',
        alertId = 0,
        alert = '',
        retryAt = '',
        retryMode = '',
        demanded = false,
        phase = '';

  final String at;
  final bool running;
  final String activeNode;
  final List<SiteCheck> results;
  final List<String> failures;
  final List<String> switched;
  final String note;
  final int alertId;
  final String alert;
  final String retryAt;
  final String retryMode;
  final bool demanded;
  final String phase;

  bool get hasRun => at.isNotEmpty;

  factory PathStatus.fromJson(Map<String, dynamic> value) => PathStatus(
        at: value['at'] as String? ?? '',
        running: value['running'] as bool? ?? false,
        activeNode: value['activeNode'] as String? ?? '',
        results: (value['results'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(SiteCheck.fromJson)
            .toList(growable: false),
        failures: (value['failures'] as List<dynamic>? ?? const [])
            .whereType<String>()
            .toList(growable: false),
        switched: (value['switched'] as List<dynamic>? ?? const [])
            .whereType<String>()
            .toList(growable: false),
        note: value['note'] as String? ?? '',
        alertId: value['alertId'] as int? ?? 0,
        alert: value['alert'] as String? ?? '',
        retryAt: value['retryAt'] as String? ?? '',
        retryMode: value['retryMode'] as String? ?? '',
        demanded: value['demanded'] as bool? ?? false,
        phase: value['phase'] as String? ?? '',
      );
}

class PathLimits {
  const PathLimits({
    required this.minIntervalSec,
    required this.maxIntervalSec,
    required this.maxTargets,
  });

  const PathLimits.fallback()
      : minIntervalSec = 30,
        maxIntervalSec = 3600,
        maxTargets = 20;

  final int minIntervalSec;
  final int maxIntervalSec;
  final int maxTargets;

  factory PathLimits.fromJson(Map<String, dynamic> value) => PathLimits(
        minIntervalSec: value['minIntervalSec'] as int? ?? 30,
        maxIntervalSec: value['maxIntervalSec'] as int? ?? 3600,
        maxTargets: value['maxTargets'] as int? ?? 20,
      );
}

/// One line for the section header: what the last round found.
String pathSummary(PathStatus status) {
  if (status.running) return '正在检测…';
  if (!status.hasRun) return '还没有检测记录';
  if (status.failures.isEmpty) return '最近一次全部通过';
  return '最近一次不通：${status.failures.join('、')}';
}

/// What the monitor did about a round that failed.
String pathSwitchLabel(PathStatus status) {
  if (status.switched.isEmpty) return '';
  final last = status.switched.last;
  return status.failures.isEmpty
      ? '因不通已切换到 $last，复检通过'
      : '已依次试过 ${status.switched.join('、')}，仍未通过';
}

/// The wall clock of a timestamp the service sent, or nothing when it has not run.
String clockLabel(String timestamp) {
  final at = DateTime.tryParse(timestamp);
  if (at == null) return '';
  final local = at.toLocal();
  String two(int value) => value.toString().padLeft(2, '0');
  return '${two(local.hour)}:${two(local.minute)}:${two(local.second)}';
}

/// The day and wall clock of a timestamp, for something refreshed every few days
/// rather than every minute.
String dayLabel(String timestamp) {
  final at = DateTime.tryParse(timestamp);
  if (at == null) return '';
  final local = at.toLocal();
  String two(int value) => value.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}';
}

/// Where domestic traffic actually goes. A subscription names the services its
/// author thought of and leaves the rest to the last rule; while that rule is
/// the proxy, a domestic site is asked to answer an address abroad. The list of
/// the country's own address ranges is what stops that.
class ChinaDirect {
  const ChinaDirect({
    required this.available,
    required this.entries,
    required this.updatedAt,
    required this.source,
    required this.error,
    required this.refreshing,
    required this.active,
  });

  const ChinaDirect.empty()
      : available = false,
        entries = 0,
        updatedAt = '',
        source = '',
        error = '',
        refreshing = false,
        active = false;

  final bool available;
  final int entries;
  final String updatedAt;
  final String source;
  final String error;
  final bool refreshing;

  /// Whether the configuration the kernel is running carries the rule. A list
  /// fetched after the kernel started is ready but not yet in effect.
  final bool active;

  ChinaDirect asRefreshing() => ChinaDirect(
        available: available,
        entries: entries,
        updatedAt: updatedAt,
        source: source,
        error: error,
        refreshing: true,
        active: active,
      );

  factory ChinaDirect.fromJson(Map<String, dynamic> value) => ChinaDirect(
        available: value['available'] as bool? ?? false,
        entries: value['entries'] as int? ?? 0,
        updatedAt: value['updatedAt'] as String? ?? '',
        source: value['source'] as String? ?? '',
        error: value['error'] as String? ?? '',
        refreshing: value['refreshing'] as bool? ?? false,
        active: value['active'] as bool? ?? false,
      );
}

/// The one line the connection page shows about the domestic rules.
String chinaDirectLabel(ChinaDirect china) {
  if (china.refreshing) return '正在获取国内地址列表…';
  if (!china.available) {
    return china.error.isEmpty
        ? '国内地址列表还没有获取：没被订阅规则点名的国内网站会被送到节点上'
        : '国内直连未生效：${china.error}';
  }
  if (!china.active) {
    return '国内直连已就绪（${china.entries} 条地址段），重新连接后生效';
  }
  return '国内直连已启用：${china.entries} 条地址段，未被订阅规则点名的国内网站直连';
}

class WintunStatus {
  const WintunStatus({
    required this.installed,
    required this.path,
    required this.version,
    required this.sha256,
    required this.detail,
  });

  final bool installed;
  final String path;
  final String version;
  final String sha256;
  final String detail;

  factory WintunStatus.fromJson(Map<String, dynamic> value) => WintunStatus(
        installed: value['installed'] as bool? ?? false,
        path: value['path'] as String? ?? '',
        version: value['version'] as String? ?? '',
        sha256: value['sha256'] as String? ?? '',
        detail: value['detail'] as String? ?? '',
      );
}

class TunStatus {
  const TunStatus({
    required this.mode,
    required this.elevated,
    required this.available,
    required this.reason,
    required this.active,
    required this.interface,
    required this.stack,
    required this.mtu,
    required this.wintun,
  });

  final String mode;
  final bool elevated;
  final bool available;
  final String reason;
  final bool active;
  final String interface;
  final String stack;
  final int mtu;
  final WintunStatus wintun;

  factory TunStatus.fromJson(Map<String, dynamic> value) => TunStatus(
        mode: value['mode'] as String? ?? 'system-proxy',
        elevated: value['elevated'] as bool? ?? false,
        available: value['available'] as bool? ?? false,
        reason: value['reason'] as String? ?? '',
        active: value['active'] as bool? ?? false,
        interface: value['interface'] as String? ?? '',
        stack: value['stack'] as String? ?? '',
        mtu: value['mtu'] as int? ?? 0,
        wintun: WintunStatus.fromJson(
            value['wintun'] as Map<String, dynamic>? ?? const {}),
      );
}

// ---------------------------------------------------------------------------
// Labels and colours. Pure, so the wording stays testable.
// ---------------------------------------------------------------------------

String regionStatusLabel(String status) => switch (status) {
      'verified' => '已验证',
      'single' => '仅一个数据源',
      'conflict' => '数据源冲突',
      'unreachable' => '不可达',
      _ => '未验证',
    };

String healthStateLabel(String state) => switch (state) {
      'HEALTHY' => '正常',
      'SUSPECT' => '可疑',
      'UNHEALTHY' => '不可用',
      'COOLDOWN' => '冷却中',
      'RECOVERY' => '恢复中',
      _ => '未知',
    };

String switchTriggerLabel(String trigger) => switch (trigger) {
      'unhealthy' => '节点不可用',
      'region_exhausted' => '同地区无可用节点，已阻断',
      'region_recovered' => '同地区恢复',
      'region_lock' => '锁定地区',
      _ => trigger,
    };

Color healthStateColor(String state) => switch (state) {
      'HEALTHY' => Tokens.ok,
      'SUSPECT' => Tokens.warn,
      'UNHEALTHY' => Tokens.bad,
      'COOLDOWN' => Tokens.idle,
      'RECOVERY' => Tokens.recovery,
      _ => Tokens.idle,
    };

/// A node the health sweep has not reached yet must not read as healthy just
/// because the service has no record for it.
String nodeHealthLabel(String? state) =>
    (state == null || state.isEmpty) ? '待巡检' : healthStateLabel(state);

Color nodeHealthDot(String? state) => (state == null || state.isEmpty)
    ? Tokens.inkFaint
    : healthStateColor(state);

String tunCheckLabel(String state) => switch (state) {
      'ok' => '通过',
      'warn' => '需注意',
      'fail' => '未通过',
      _ => state,
    };

Color tunCheckColor(String state) => switch (state) {
      'ok' => Tokens.ok,
      'warn' => Tokens.warn,
      'fail' => Tokens.bad,
      _ => Tokens.idle,
    };

Color siteStateColor(String state) => switch (state) {
      'ok' => Tokens.ok,
      'redirect' => Tokens.warn,
      'restricted' => Tokens.warn,
      // A pass that stopped early must not look like a measured failure.
      'skipped' => Tokens.inkFaint,
      _ => Tokens.bad,
    };

String siteStateLabel(String state) => switch (state) {
      'ok' => '可访问',
      'redirect' => '可达，发生跳转',
      'restricted' => '访问受限',
      'skipped' => '未检测',
      _ => '连接失败',
    };

// A node that does not relay UDP is a warning rather than a failure: QUIC is
// refused up front, so clients fall back to TCP and keep working.
String udpRelayLabel(bool reachable) => reachable ? '可用' : '不可用';

Color udpRelayColor(bool reachable) => reachable ? Tokens.ok : Tokens.warn;

String formatLabel(String format) => switch (format) {
      'clash-yaml' => 'Clash YAML',
      'base64-clash-yaml' => 'Base64 包裹的 Clash YAML',
      'uri-list' => '节点链接列表',
      'base64-uri-list' => 'Base64 包裹的节点链接列表',
      'empty' => '空响应',
      _ => '无法识别',
    };

String byteLabel(int bytes) {
  if (bytes < 1024) return '$bytes B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  var value = bytes / 1024;
  var unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return '${value.toStringAsFixed(1)} ${units[unit]}';
}

/// A rate of zero is "nothing moving" rather than 0 B/s, which reads like a
/// measurement that failed.
String rateLabel(int bytesPerSecond) =>
    bytesPerSecond <= 0 ? '—' : '${byteLabel(bytesPerSecond)}/s';

String durationLabel(int milliseconds) {
  final seconds = milliseconds ~/ 1000;
  if (seconds < 1) return '刚刚建立';
  if (seconds < 60) return '$seconds 秒';
  if (seconds < 3600) return '${seconds ~/ 60} 分 ${seconds % 60} 秒';
  return '${seconds ~/ 3600} 时 ${(seconds % 3600) ~/ 60} 分';
}

/// The choices the close prompt offers. Hiding is only offered while the tray
/// icon is registered, because hiding a window with no icon would leave the user
/// with no way back to it.
enum CloseChoice { hide, quit, cancel }

List<CloseChoice> closeChoices({required bool trayReady}) => [
      if (trayReady) CloseChoice.hide,
      CloseChoice.quit,
      CloseChoice.cancel,
    ];

String closeChoiceLabel(CloseChoice choice) => switch (choice) {
      CloseChoice.hide => '最小化到托盘',
      CloseChoice.quit => '退出 SmartVPN',
      CloseChoice.cancel => '取消',
    };

/// What the window should do about an answer, which is the runner's vocabulary
/// rather than the user's.
String closeChoiceAction(CloseChoice choice) => switch (choice) {
      CloseChoice.hide => 'hide',
      CloseChoice.quit => 'quit',
      CloseChoice.cancel => 'cancel',
    };

String trayTooltip({required bool connected, required String node}) {
  if (!connected) return 'SmartVPN · 未连接';
  return node.isEmpty ? 'SmartVPN · 已连接' : 'SmartVPN · 已连接 · $node';
}

String uriCountLabel(dynamic raw) {
  if (raw is! Map) return '0';
  final parts = <String>[];
  raw.forEach((key, value) {
    if (value is int && value > 0) parts.add('$key $value');
  });
  return parts.isEmpty ? '0' : parts.join('，');
}

String countsLabel(Map<String, dynamic> values) {
  final parts = <String>[];
  values.forEach((key, value) {
    if (value is int && value > 0) parts.add('$key $value');
  });
  return parts.isEmpty ? '无' : parts.join('，');
}

// ---------------------------------------------------------------------------
// Node grouping for the node table.
// ---------------------------------------------------------------------------

enum NodeGrouping { region, line, source }

class NodeGroup {
  const NodeGroup({required this.label, required this.nodes});

  final String label;
  final List<ProxyNode> nodes;
}

/// The line style a provider encodes in the node name. Checked in this order so
/// a name carrying two markers lands in the more specific bucket.
const _lineKinds = ['流媒体', '中转', '优化', '直连'];

String nodeLineKind(String name) {
  for (final kind in _lineKinds) {
    if (name.contains(kind)) return kind;
  }
  return '其他';
}

/// Extracts subscription prefix (e.g. "[主力]") from the node name.
String nodeSourceKind(ProxyNode node) {
  if (node.manual) return '手动节点';
  final trimmed = node.name.trim();
  if (trimmed.startsWith('[')) {
    final end = trimmed.indexOf(']');
    if (end > 1) {
      return trimmed.substring(0, end + 1);
    }
  }
  return '默认源';
}

int _lineRank(String kind) {
  final index = _lineKinds.indexOf(kind);
  return index == -1 ? _lineKinds.length : index;
}

/// Groups nodes for display. Regions sort alphabetically with the unverified
/// bucket last; line kinds keep the provider's own order; source groups keep
/// named prefixes first, with default/manual last.
List<NodeGroup> groupNodes({
  required List<ProxyNode> nodes,
  required Map<String, RegionInfo> regions,
  required NodeGrouping grouping,
}) {
  final buckets = <String, List<ProxyNode>>{};
  for (final node in nodes) {
    final region = regions[node.name];
    final key = switch (grouping) {
      NodeGrouping.region =>
        (region != null && region.usable) ? region.country : '未验证',
      NodeGrouping.line => nodeLineKind(node.name),
      NodeGrouping.source => nodeSourceKind(node),
    };
    buckets.putIfAbsent(key, () => <ProxyNode>[]).add(node);
  }
  final labels = buckets.keys.toList()
    ..sort((a, b) {
      if (grouping == NodeGrouping.line) {
        return _lineRank(a).compareTo(_lineRank(b));
      }
      if (grouping == NodeGrouping.source) {
        final rankA = a == '默认源' ? 1 : (a == '手动节点' ? 2 : 0);
        final rankB = b == '默认源' ? 1 : (b == '手动节点' ? 2 : 0);
        if (rankA != rankB) return rankA.compareTo(rankB);
        return a.compareTo(b);
      }
      if (a == '未验证') return 1;
      if (b == '未验证') return -1;
      return a.compareTo(b);
    });
  return [
    for (final label in labels) NodeGroup(label: label, nodes: buckets[label]!),
  ];
}

bool nodeMatchesQuery(ProxyNode node, String query) {
  final needle = query.trim().toLowerCase();
  if (needle.isEmpty) return true;
  return node.name.toLowerCase().contains(needle);
}

// ---------------------------------------------------------------------------
// What the app claims about protection. Pure, so the conditions and the wording
// live in one place instead of being spread across the widgets.
// ---------------------------------------------------------------------------

String protectionStateLabel({
  required bool connected,
  required bool blocked,
  required String mode,
  required bool tunActive,
  required bool proxyEnabled,
}) {
  if (!connected) return '未连接';
  if (blocked) return '受保护流量已阻断';
  if (mode == 'vpn') {
    // Android carries everything through the system's VPN service, so there is
    // no setting to have got out of step: either the tunnel is up or it is not.
    return tunActive ? '已连接 · VPN 全设备' : '内核运行中，VPN 隧道未就绪';
  }
  if (mode == 'tun') {
    return tunActive ? '已连接 · TUN 全设备' : '内核运行中，TUN 未就绪';
  }
  return proxyEnabled ? '已连接 · 系统代理' : '内核运行中，系统代理未启用';
}

String protectionStateDetail({
  required bool connected,
  required bool blocked,
  required String mode,
  required bool tunActive,
  required bool proxyEnabled,
}) {
  if (!connected) return '连接后本机流量按所选节点转发。';
  if (blocked) return '同地区没有可用节点，SmartVPN 不会跨地区直连，正在等待节点恢复。';
  if (mode == 'vpn') {
    return tunActive
        ? '系统 VPN 已接管全部应用的流量，它们都经过所选节点。'
        : 'VPN 隧道尚未就绪，连接时会先向系统申请授权。';
  }
  if (mode == 'tun') {
    return tunActive
        ? 'TUN 已接管系统流量，不使用系统代理的程序也在隧道内。'
        : 'TUN 尚未接管流量，点击连接会先检查管理员权限与 wintun.dll。';
  }
  return proxyEnabled
      ? 'Windows 系统代理已指向本机内核，遵循系统代理的程序正在使用所选节点。'
      : 'Windows 系统代理已关闭或被其他程序修改，浏览器可能没有经过 SmartVPN。';
}

/// The exit address readout on the connection page.
String exitAddressLabel({required String ipv4, required String ipv4Error}) {
  if (ipv4.isNotEmpty) return ipv4;
  if (ipv4Error.isNotEmpty) return '未取得';
  return '—';
}
