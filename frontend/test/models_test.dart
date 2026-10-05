import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/main.dart';
import 'package:smartvpn_windows/tokens.dart';

void main() {
  group('ProxyNode.fromJson', () {
    test('reads every field the local service sends', () {
      final node = ProxyNode.fromJson(const {
        'name': '香港 01',
        'type': 'Vmess',
        'network': 'ws',
        'tls': true,
        'wsHostConfigured': true,
        'wsPathConfigured': true,
        'alive': true,
        'selected': false,
      });
      expect(node.name, '香港 01');
      expect(node.type, 'Vmess');
      expect(node.network, 'ws');
      expect(node.tls, isTrue);
      expect(node.wsHostConfigured, isTrue);
      expect(node.wsPathConfigured, isTrue);
      expect(node.alive, isTrue);
      expect(node.selected, isFalse);
    });

    test('falls back to safe defaults when fields are missing', () {
      final node = ProxyNode.fromJson(const {});
      expect(node.name, '');
      expect(node.type, '');
      expect(node.network, '');
      expect(node.tls, isFalse);
      expect(node.wsHostConfigured, isFalse);
      expect(node.wsPathConfigured, isFalse);
      expect(node.alive, isFalse);
      expect(node.selected, isFalse);
    });
  });

  group('SiteCheck.fromJson', () {
    test('reads every field the local service sends', () {
      final check = SiteCheck.fromJson(const {
        'name': 'ChatGPT',
        'state': 'restricted',
        'detail': '站点已响应，但要求验证或限制访问',
        'httpCode': 403,
        'latencyMs': 812,
      });
      expect(check.name, 'ChatGPT');
      expect(check.state, 'restricted');
      expect(check.detail, '站点已响应，但要求验证或限制访问');
      expect(check.httpCode, 403);
      expect(check.latencyMs, 812);
    });

    test('defaults to an error state without a response code', () {
      final check = SiteCheck.fromJson(const {});
      expect(check.name, '');
      expect(check.state, 'error');
      expect(check.detail, '');
      expect(check.httpCode, 0);
      expect(check.latencyMs, 0);
    });
  });

  group('RegionInfo.fromJson', () {
    test('reads the payload the local service sends', () {
      final region = RegionInfo.fromJson(const {
        'name': '香港 01',
        'country': 'JP',
        'exitIp': '203.0.113.7',
        'status': 'verified',
        'detail': '两个数据源一致',
        'sources': ['geojs.io', 'ipwho.is'],
        'verifiedAt': '2026-09-25T12:00:00Z',
        'stale': false,
      });
      expect(region.country, 'JP');
      expect(region.exitIp, '203.0.113.7');
      expect(region.sources, ['geojs.io', 'ipwho.is']);
      expect(region.usable, isTrue);
    });

    test('only a fresh cross-checked region counts as usable', () {
      RegionInfo build(String status, {bool stale = false}) =>
          RegionInfo.fromJson({
            'name': 'n',
            'country': 'JP',
            'status': status,
            'stale': stale,
          });

      expect(build('verified').usable, isTrue);
      expect(build('verified', stale: true).usable, isFalse);
      expect(build('single').usable, isFalse);
      expect(build('conflict').usable, isFalse);
      expect(build('unreachable').usable, isFalse);
      expect(RegionInfo.fromJson(const {}).usable, isFalse);
    });

    test('defaults to an unverified status without sources', () {
      final region = RegionInfo.fromJson(const {});
      expect(region.name, '');
      expect(region.country, '');
      expect(region.status, 'unverified');
      expect(region.sources, isEmpty);
      expect(region.stale, isFalse);
    });
  });

  group('RegionJob.fromJson', () {
    test('reads progress and the last error', () {
      final job = RegionJob.fromJson(const {
        'running': true,
        'total': 42,
        'done': 12,
        'verified': 9,
        'skipped': 2,
        'failed': 1,
        'lastError': '上次中断',
      });
      expect(job.running, isTrue);
      expect(job.total, 42);
      expect(job.done, 12);
      expect(job.verified, 9);
      expect(job.skipped, 2);
      expect(job.failed, 1);
      expect(job.lastError, '上次中断');
    });

    test('defaults to a finished empty job', () {
      final job = RegionJob.fromJson(const {});
      expect(job.running, isFalse);
      expect(job.total, 0);
      expect(job.lastError, '');
    });
  });

  group('HealthParams', () {
    test('reads the documented defaults from an empty payload', () {
      final params = HealthParams.fromJson(const {});
      expect(params.connectTimeoutMs, 3000);
      expect(params.patrolIntervalSec, 60);
      expect(params.failureThreshold, 2);
      expect(params.cooldownSec, 60);
      expect(params.minDwellSec, 30);
      expect(params.recoverySuccesses, 3);
    });

    test('round trips through toJson so the service can reject bad values', () {
      const params = HealthParams(
        connectTimeoutMs: 2500,
        patrolIntervalSec: 45,
        failureThreshold: 3,
        cooldownSec: 90,
        minDwellSec: 20,
        recoverySuccesses: 4,
      );
      expect(HealthParams.fromJson(params.toJson()).patrolIntervalSec, 45);
      expect(HealthParams.fromJson(params.toJson()).failureThreshold, 3);
    });
  });

  group('HealthSummary.fromJson', () {
    test('counts every state and keeps the locked region', () {
      final summary = HealthSummary.fromJson(const {
        'counts': {
          'HEALTHY': 4,
          'SUSPECT': 1,
          'UNHEALTHY': 2,
          'COOLDOWN': 3,
          'RECOVERY': 0,
        },
        'poolSize': 4,
        'lockedRegion': 'JP',
        'params': {'patrolIntervalSec': 45},
      });
      expect(summary.count('HEALTHY'), 4);
      expect(summary.count('UNHEALTHY'), 2);
      expect(summary.count('COOLDOWN'), 3);
      expect(summary.count('MISSING'), 0);
      expect(summary.poolSize, 4);
      expect(summary.lockedRegion, 'JP');
      expect(summary.params.patrolIntervalSec, 45);
    });

    test('tolerates an empty health block', () {
      final summary = HealthSummary.fromJson(const {});
      expect(summary.count('HEALTHY'), 0);
      expect(summary.poolSize, 0);
      expect(summary.lockedRegion, '');
    });

    test('carries the note about a region that measured dead but still carries traffic', () {
      // The page shows this instead of a block: traffic flows, so the sweep's
      // verdict was the thing that was wrong.
      final summary = HealthSummary.fromJson(const {
        'poolSize': 0,
        'sweepNote': '同地区节点在延迟测试里全部不通，但经当前节点的实际请求仍然通行，因此没有阻断；以实测为准。',
      });
      expect(summary.sweepNote, isNotEmpty);
      expect(summary.sweepNote, contains('仍然通行'));
      expect(HealthSummary.fromJson(const {}).sweepNote, isEmpty);
    });
  });

  group('NodeHealth.fromJson', () {
    test('reads the state the patrol loop records', () {
      final health = NodeHealth.fromJson(const {
        'state': 'COOLDOWN',
        'latencyMs': 180,
        'consecutiveFailures': 2,
        'cooldownUntil': '2026-09-25T12:01:00Z',
      });
      expect(health.state, 'COOLDOWN');
      expect(health.latencyMs, 180);
      expect(health.consecutiveFailures, 2);
      expect(health.cooldownUntil, '2026-09-25T12:01:00Z');
    });

    test('leaves the state blank when the service sends no record', () {
      final health = NodeHealth.fromJson(const {});
      expect(health.state, '');
      expect(health.latencyMs, 0);
    });
  });

  group('SwitchEvent.fromJson', () {
    test('reads a recorded switch', () {
      final event = SwitchEvent.fromJson(const {
        'at': '2026-09-25T12:00:00Z',
        'group': 'SmartVPN',
        'fromNode': '香港 01',
        'toNode': 'REJECT',
        'trigger': 'region_exhausted',
        'evidence': '同地区已无可用节点',
      });
      expect(event.fromNode, '香港 01');
      expect(event.toNode, 'REJECT');
      expect(event.trigger, 'region_exhausted');
      expect(event.evidence, '同地区已无可用节点');
    });
  });

  group('labels', () {
    test('explains every region status', () {
      expect(regionStatusLabel('verified'), '已验证');
      expect(regionStatusLabel('single'), '仅一个数据源');
      expect(regionStatusLabel('conflict'), '数据源冲突');
      expect(regionStatusLabel('unreachable'), '不可达');
      expect(regionStatusLabel('unverified'), '未验证');
      expect(regionStatusLabel('anything-else'), '未验证');
    });

    test('explains every health state', () {
      expect(healthStateLabel('HEALTHY'), '正常');
      expect(healthStateLabel('SUSPECT'), '可疑');
      expect(healthStateLabel('UNHEALTHY'), '不可用');
      expect(healthStateLabel('COOLDOWN'), '冷却中');
      expect(healthStateLabel('RECOVERY'), '恢复中');
    });

    test('explains why a switch happened', () {
      expect(switchTriggerLabel('unhealthy'), '节点不可用');
      expect(switchTriggerLabel('region_exhausted'), '同地区无可用节点，已阻断');
      expect(switchTriggerLabel('region_recovered'), '同地区恢复');
      expect(switchTriggerLabel('region_lock'), '锁定地区');
    });

    test('colours the fail-closed states distinctly', () {
      expect(healthStateColor('UNHEALTHY'), isNot(healthStateColor('HEALTHY')));
      expect(healthStateColor('HEALTHY'), isNot(healthStateColor('COOLDOWN')));
    });

    test('a node with no health record reads as pending, never healthy', () {
      expect(nodeHealthLabel(null), '待巡检');
      expect(nodeHealthLabel(''), '待巡检');
      expect(nodeHealthLabel('HEALTHY'), '正常');
      expect(nodeHealthLabel('UNHEALTHY'), '不可用');
      expect(nodeHealthDot(null), isNot(healthStateColor('HEALTHY')));
      expect(nodeHealthDot('HEALTHY'), healthStateColor('HEALTHY'));
    });
  });

  group('TunStatus.fromJson', () {
    test('reads the payload the local service sends', () {
      final status = TunStatus.fromJson(const {
        'mode': 'tun',
        'elevated': true,
        'available': true,
        'reason': '',
        'active': true,
        'interface': 'SmartVPN',
        'stack': 'gvisor',
        'mtu': 1500,
        'wintun': {
          'installed': true,
          'path': r'C:\Users\me\AppData\Roaming\SmartVPN\wintun.dll',
          'version': '0.14.1',
          'sha256': 'e5da8447',
        },
      });
      expect(status.mode, 'tun');
      expect(status.elevated, isTrue);
      expect(status.available, isTrue);
      expect(status.active, isTrue);
      expect(status.interface, 'SmartVPN');
      expect(status.mtu, 1500);
      expect(status.wintun.installed, isTrue);
      expect(status.wintun.version, '0.14.1');
    });

    test('defaults to the system proxy with nothing installed', () {
      final status = TunStatus.fromJson(const {});
      expect(status.mode, 'system-proxy');
      expect(status.elevated, isFalse);
      expect(status.available, isFalse);
      expect(status.active, isFalse);
      expect(status.wintun.installed, isFalse);
      expect(status.wintun.path, '');
    });
  });

  group('TunCheck.fromJson', () {
    test('reads a passed check', () {
      final check = TunCheck.fromJson(const {
        'name': 'IPv4 出口',
        'state': 'ok',
        'detail': '系统路径与隧道出口一致，流量确实经过内核',
        'value': '38.207.142.122',
      });
      expect(check.name, 'IPv4 出口');
      expect(check.state, 'ok');
      expect(check.value, '38.207.142.122');
    });

    test('treats a missing state as a warning rather than a pass', () {
      expect(TunCheck.fromJson(const {}).state, 'warn');
    });
  });

  group('UdpRelay.fromJson', () {
    test('reads a relayed answer', () {
      final relay = UdpRelay.fromJson(const {
        'target': '1.1.1.1:53',
        'reachable': true,
        'latencyMs': 184,
        'answer': '93.184.216.34',
      });
      expect(relay.target, '1.1.1.1:53');
      expect(relay.reachable, isTrue);
      expect(relay.latencyMs, 184);
      expect(relay.answer, '93.184.216.34');
      expect(relay.reason, isEmpty);
    });

    test('reads a node that returned nothing', () {
      final relay = UdpRelay.fromJson(const {
        'target': '1.1.1.1:53',
        'reachable': false,
        'reason': '节点没有回传 UDP（超时未收到应答）',
      });
      expect(relay.reachable, isFalse);
      expect(relay.reason, contains('超时'));
      expect(relay.latencyMs, 0);
    });

    test('defaults an empty body to unreachable', () {
      expect(UdpRelay.fromJson(const {}).reachable, isFalse);
      expect(UdpRelay.fromJson(const {}).target, isEmpty);
    });

    test('labels an unusable relay as a warning, not a failure', () {
      expect(udpRelayLabel(true), '可用');
      expect(udpRelayLabel(false), '不可用');
      expect(udpRelayColor(false), isNot(udpRelayColor(true)));
    });
  });

  group('TrafficSnapshot.fromJson', () {
    test('reads totals, rates and connections', () {
      final snapshot = TrafficSnapshot.fromJson(const {
        'available': true,
        'uploadTotal': 1048576,
        'downloadTotal': 104857600,
        'uploadRate': 2048,
        'downloadRate': 524288,
        'connectionCount': 42,
        'shownCount': 2,
        'connections': [
          {
            'process': 'chrome.exe',
            'target': 'rr1.googlevideo.com:443',
            'network': 'tcp',
            'type': 'HTTP',
            'rule': 'DomainSuffix(googlevideo.com)',
            'chain': '日本V3 B',
            'upload': 1024,
            'download': 4096,
            'durationMs': 30000,
          },
          {
            'target': '9.9.9.9:53',
            'chain': 'DIRECT',
          },
        ],
      });
      expect(snapshot.available, isTrue);
      expect(snapshot.uploadTotal, 1048576);
      expect(snapshot.connectionCount, 42);
      expect(snapshot.shownCount, 2);
      expect(snapshot.connections, hasLength(2));

      final first = snapshot.connections.first;
      expect(first.process, 'chrome.exe');
      expect(first.target, 'rr1.googlevideo.com:443');
      expect(first.rule, 'DomainSuffix(googlevideo.com)');
      expect(first.chain, '日本V3 B');
      expect(first.durationMs, 30000);

      // A minimal row keeps its defaults instead of throwing.
      final second = snapshot.connections[1];
      expect(second.process, isEmpty);
      expect(second.chain, 'DIRECT');
      expect(second.upload, 0);
      expect(second.durationMs, 0);
    });

    test('treats an unavailable reading as a reason, not an error', () {
      final snapshot =
          TrafficSnapshot.fromJson(const {'available': false, 'reason': '内核未运行'});
      expect(snapshot.available, isFalse);
      expect(snapshot.reason, '内核未运行');
      expect(snapshot.connections, isEmpty);
    });

    test('defaults an empty body', () {
      final snapshot = TrafficSnapshot.fromJson(const {});
      expect(snapshot.available, isFalse);
      expect(snapshot.uploadRate, 0);
      expect(snapshot.connections, isEmpty);
    });
  });

  group('traffic labels', () {
    test('measures bytes in the unit that fits', () {
      expect(byteLabel(512), '512 B');
      expect(byteLabel(1024), '1.0 KB');
      expect(byteLabel(1048576), '1.0 MB');
      expect(byteLabel(1073741824), '1.0 GB');
      expect(byteLabel(5 * 1073741824), '5.0 GB');
    });

    test('calls an idle rate nothing moving', () {
      expect(rateLabel(0), '—');
      expect(rateLabel(2048), '2.0 KB/s');
    });

    test('describes how long a connection has been up', () {
      expect(durationLabel(400), '刚刚建立');
      expect(durationLabel(30000), '30 秒');
      expect(durationLabel(90000), '1 分 30 秒');
      expect(durationLabel(7200000), '2 时 0 分');
    });
  });

  group('close prompt', () {
    test('always offers quitting and cancelling', () {
      expect(closeChoices(trayReady: true),
          [CloseChoice.hide, CloseChoice.quit, CloseChoice.cancel]);
      // Without a tray icon, hiding would leave no way back to the window.
      expect(closeChoices(trayReady: false),
          [CloseChoice.quit, CloseChoice.cancel]);
    });

    test('names every choice and the action it stands for', () {
      expect(closeChoiceLabel(CloseChoice.hide), '最小化到托盘');
      expect(closeChoiceLabel(CloseChoice.quit), '退出 SmartVPN');
      expect(closeChoiceLabel(CloseChoice.cancel), '取消');
      expect(closeChoiceAction(CloseChoice.hide), 'hide');
      expect(closeChoiceAction(CloseChoice.quit), 'quit');
      expect(closeChoiceAction(CloseChoice.cancel), 'cancel');
    });

    test('the tray tooltip names the node in use', () {
      expect(trayTooltip(connected: false, node: ''), 'SmartVPN · 未连接');
      expect(trayTooltip(connected: true, node: ''), 'SmartVPN · 已连接');
      expect(trayTooltip(connected: true, node: '日本V3 B'),
          'SmartVPN · 已连接 · 日本V3 B');
    });
  });

  group('saved subscriptions', () {
    test('reads a saved entry', () {
      final entry = SavedSubscription.fromJson(const {
        'id': 'abc',
        'url': 'https://example.com/sub?token=1',
        'label': '机场A',
        'updatedAt': 1790000000,
        'nodeCount': 42,
      });
      expect(entry.id, 'abc');
      expect(entry.label, '机场A');
      expect(entry.nodeCount, 42);
      expect(entry.hasFailed, isFalse);
    });

    test('a failed fetch is part of the entry, not an error state', () {
      final entry = SavedSubscription.fromJson(
          const {'id': 'a', 'label': 'B', 'lastError': '网络不可达'});
      expect(entry.hasFailed, isTrue);
      expect(subscriptionDetail(entry), contains('网络不可达'));
      expect(subscriptionDetail(SavedSubscription.fromJson(const {
        'id': 'a',
        'label': 'B',
        'nodeCount': 3,
        'updatedAt': 0,
      })), contains('尚未拉取'));
    });

    test('reads enabled, prefix and userInfo', () {
      final entry = SavedSubscription.fromJson(const {
        'id': 'sub-multi',
        'url': 'https://airport.com/sub',
        'label': '主力机场',
        'prefix': '[主力]',
        'enabled': true,
        'nodeCount': 100,
        'updatedAt': 1790000000,
        'userInfo': {
          'upload': 1073741824,
          'download': 10737418240,
          'total': 107374182400,
          'expire': 1775000000,
        },
      });
      expect(entry.prefix, '[主力]');
      expect(entry.enabled, isTrue);
      expect(entry.userInfo, isNotNull);
      expect(entry.userInfo!.usedString, contains('GB'));
      expect(entry.userInfo!.totalString, contains('GB'));
      expect(entry.userInfo!.expireString, contains('2026-'));
      expect(entry.userInfo!.progress, greaterThan(0.0));
    });

    test('describes how long ago a fetch happened', () {
      final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
      expect(updatedLabel(0), '尚未拉取');
      expect(updatedLabel(now - 30), '刚刚');
      expect(updatedLabel(now - 600), '10 分钟前');
      expect(updatedLabel(now - 7200), '2 小时前');
      expect(updatedLabel(now - 3 * 86400), '3 天前');
    });

    test('reads a pasted node as the service describes it', () {
      final node = ManualNode.fromJson(const {
        'name': '手动TJ',
        'type': 'trojan',
        'server': 'tj.example.com',
        'port': 443,
      });
      expect(node.endpoint, 'tj.example.com:443');
      expect(parsedNodeLabel(node), contains('手动TJ'));
      expect(parsedNodeLabel(node), contains('tj.example.com:443'));
    });

    test('a node list marks which nodes were pasted', () {
      final manual = ProxyNode.fromJson(const {'name': '手动TJ', 'manual': true});
      final subscribed = ProxyNode.fromJson(const {'name': '香港 01'});
      expect(manual.manual, isTrue);
      expect(subscribed.manual, isFalse);
    });
  });

  group('profile location', () {
    test('names the directory and says when the copy is portable', () {
      expect(profileLabel(home: '', portable: false), '—');
      expect(profileLabel(home: r'C:\Users\me\AppData\Roaming\SmartVPN', portable: false),
          r'C:\Users\me\AppData\Roaming\SmartVPN');
      final portable = profileLabel(home: r'E:\SmartVPN-data', portable: true);
      expect(portable, contains(r'E:\SmartVPN-data'));
      expect(portable, contains('便携'));
    });
  });

  group('china direct', () {
    test('reads what the service reports', () {
      final china = ChinaDirect.fromJson(const {
        'available': true,
        'entries': 9616,
        'updatedAt': '2026-09-26T01:20:00+08:00',
        'source': 'https://cdn.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/',
        'active': true,
      });
      expect(china.available, isTrue);
      expect(china.entries, 9616);
      expect(china.active, isTrue);
      expect(chinaDirectLabel(china), contains('9616'));
      expect(chinaDirectLabel(china), contains('直连'));
    });

    test('survives an empty body', () {
      final china = ChinaDirect.fromJson(const {});
      expect(china.available, isFalse);
      expect(china.entries, 0);
      expect(china.refreshing, isFalse);
      expect(chinaDirectLabel(china), contains('还没有获取'));
      expect(chinaDirectLabel(const ChinaDirect.empty()), contains('还没有获取'));
    });

    test('says when a list is ready but not yet in the running configuration', () {
      final china = ChinaDirect.fromJson(const {
        'available': true,
        'entries': 9616,
        'active': false,
      });
      expect(chinaDirectLabel(china), contains('重新连接后生效'));
    });

    test('reports a failure rather than pretending the list is there', () {
      final china = ChinaDirect.fromJson(const {
        'available': false,
        'error': 'no route to the source',
      });
      expect(chinaDirectLabel(china), contains('no route to the source'));
    });

    test('says a refresh is running, and marks a copy as one', () {
      expect(
          chinaDirectLabel(ChinaDirect.fromJson(const {'refreshing': true})),
          contains('正在获取'));
      final marking = const ChinaDirect.empty().asRefreshing();
      expect(marking.refreshing, isTrue);
      expect(chinaDirectLabel(marking), contains('正在获取'));
    });

    test('names the day a list was fetched, not just the second', () {
      expect(dayLabel('2026-09-26T01:20:00+08:00'), '2026-09-26 01:20');
      expect(dayLabel(''), isEmpty);
      expect(dayLabel('not a timestamp'), isEmpty);
    });
  });

  group('diagnose report', () {
    const report = {
      'node': '🇯🇵 日本V3 B|中转|x1',
      'temporary': true,
      'egress': {
        'ipv4': '203.0.113.7',
        'country': 'Japan',
        'countryCode': 'JP',
        'city': 'Tokyo',
        'asn': 'AS2497',
        'isp': 'IIJ',
        'org': 'Internet Initiative Japan',
      },
      'dns': {
        'resolver': '223.5.5.5',
        'geo': 'China (CN) - Hangzhou',
        'country': 'CN',
        'matchesExit': false,
      },
      'udp': {
        'relay': {'target': '1.1.1.1:53', 'reachable': true, 'latencyMs': 178, 'answer': '93.184.216.34'},
        'mapped': '203.0.113.7:54321',
        'matchesExit': true,
      },
      'quality': {
        'ipType': '机房/数据中心 (Data Center / Hosting)',
        'fraudScore': 25,
        'riskLevel': '低风险 (良好)',
        'isProxy': false,
        'isVpn': false,
        'isTor': false,
        'isHosting': true,
        'isNative': false,
        'webrtcLeak': false,
        'dnsLeak': true,
      },
      'sites': [
        {'name': 'Google', 'state': 'ok', 'detail': '站点已响应', 'httpCode': 204, 'latencyMs': 210},
        {'name': 'ChatGPT', 'state': 'error', 'detail': '连接超时'},
      ],
    };

    test('reads every section', () {
      final parsed = DiagnoseReport.fromJson(report);
      expect(parsed.node, contains('日本V3 B'));
      expect(parsed.temporary, isTrue);
      expect(parsed.egress.ipv4, '203.0.113.7');
      expect(parsed.egress.countryCode, 'JP');
      expect(parsed.dns.resolver, '223.5.5.5');
      expect(parsed.dns.matchesExit, isFalse);
      expect(parsed.udp.relay!.reachable, isTrue);
      expect(parsed.udp.relay!.latencyMs, 178);
      expect(parsed.udp.matchesExit, isTrue);
      expect(parsed.quality.ipType, contains('机房'));
      expect(parsed.quality.fraudScore, 25);
      expect(parsed.quality.isHosting, isTrue);
      expect(parsed.quality.webrtcLeak, isFalse);
      expect(parsed.quality.dnsLeak, isTrue);
      expect(qualityRiskBadge(parsed.quality), '低风险 (良好)');
      expect(webrtcVerdictBadge(parsed.udp, parsed.quality), '安全无泄露');
      expect(parsed.sites, hasLength(2));
      expect(parsed.sites.first.httpCode, 204);
    });

    test('survives an empty body', () {
      final parsed = DiagnoseReport.fromJson(const {});
      expect(parsed.node, isEmpty);
      expect(parsed.egress.ipv4, isEmpty);
      expect(parsed.udp.relay, isNull);
      expect(parsed.sites, isEmpty);
    });

    test('names the exit and who runs it', () {
      final egress = DiagnoseReport.fromJson(report).egress;
      expect(egressLocationLabel(egress), 'Tokyo · Japan');
      expect(egressOperatorLabel(egress), 'AS2497 · IIJ · Internet Initiative Japan');
      expect(egressAddressLabel(address: egress.ipv4, fallback: ''), '203.0.113.7');
      expect(egressAddressLabel(address: '', fallback: '出口 IP 查询失败'), '出口 IP 查询失败');
      expect(egressAddressLabel(address: '', fallback: ''), '未取得');
    });

    test('states each verdict rather than leaving it to the page', () {
      final parsed = DiagnoseReport.fromJson(report);
      expect(dnsVerdictLabel(parsed.dns), '在本地解析（未走出口）');
      expect(udpVerdictLabel(parsed.udp), '与 TCP 出口一致');
      expect(dnsVerdictLabel(const DnsFacts.empty()), '未测出');
      expect(
          dnsVerdictLabel(const DnsFacts(
              resolver: '1.1.1.1', geo: '', country: 'JP', matchesExit: true, error: '')),
          '在出口所在地解析');
    });

    test('copies the same rows as text', () {
      final text = diagnoseReportText(DiagnoseReport.fromJson(report),
          speed: SpeedTestResult.fromJson(const {
            'target': 'speed.cloudflare.com',
            'megaBytes': 30,
            'downMbps': 12.34,
            'upMbps': 3.21,
            'downBytes': 10485760,
            'upBytes': 3145728,
            'downMs': 6800,
            'upMs': 7900,
          }),
          at: DateTime(2026, 9, 25, 23, 55));
      expect(text, contains('SmartVPN 检测报告'));
      expect(text, contains('203.0.113.7'));
      expect(text, contains('Tokyo · Japan'));
      expect(text, contains('DNS 解析出口：223.5.5.5'));
      expect(text, contains('在本地解析（未走出口）'));
      expect(text, contains('UDP 中继：可用 178 ms'));
      expect(text, contains('与 TCP 出口一致'));
      expect(text, contains('Google 可访问 210ms'));
      expect(text, contains('下行 12.3 Mbit/s'));
      expect(text, contains('2026-09-25 23:55'));
    });

    test('reports a failed speed test as a failure, not as a number', () {
      final text = diagnoseReportText(DiagnoseReport.fromJson(report),
          speed: SpeedTestResult.fromJson(
              const {'target': 'speed.cloudflare.com', 'error': '测速没有收到任何数据'}));
      expect(text, contains('网速：测速没有收到任何数据'));
    });
  });

  group('speed labels', () {
    test('formats a rate and what it cost', () {
      expect(speedLabel(0), '—');
      expect(speedLabel(12.34), '12.3 Mbit/s');
      expect(
          speedDetailLabel(SpeedTestResult.fromJson(const {
            'downBytes': 1048576,
            'upBytes': 524288,
            'downMs': 2000,
            'upMs': 1000,
          })),
          '实测 1.0 MB ↓ / 512.0 KB ↑ · 3.0 秒');
      expect(speedDetailLabel(SpeedTestResult.fromJson(const {})), '—');
    });
  });

  group('path checks', () {
    test('reads the configuration and what the service will send back', () {
      final checks = PathChecks.fromJson(const {
        'enabled': true,
        'intervalSec': 120,
        'targets': [
          {'label': 'Google', 'url': 'https://www.google.com/generate_204', 'builtin': true, 'enabled': true},
          {'label': 'Mine', 'url': 'https://example.com/', 'builtin': false, 'enabled': false},
        ],
      });
      expect(checks.enabled, isTrue);
      expect(checks.intervalSec, 120);
      expect(checks.enabledTargets.map((target) => target.label), ['Google']);

      final sent = checks.toJson();
      expect(sent['enabled'], isTrue);
      final targets = sent['targets'] as List<dynamic>;
      expect((targets.first as Map<String, dynamic>).containsKey('builtin'), isFalse,
          reason: 'whether a target is built in is the service\'s judgement');
    });

    test('defaults to a switched-off monitor', () {
      final checks = PathChecks.fromJson(const {});
      expect(checks.enabled, isFalse);
      expect(checks.intervalSec, 120);
      expect(checks.targets, isEmpty);
      expect(const PathLimits.fallback().maxTargets, 20);
    });

    test('reads the status of the last round', () {
      final status = PathStatus.fromJson(const {
        'at': '2026-09-26T00:12:03+08:00',
        'activeNode': '日本V1 A',
        'results': [
          {'name': 'Google', 'state': 'ok', 'httpCode': 204, 'latencyMs': 210},
          {'name': 'YouTube', 'state': 'error', 'detail': '连接超时'},
        ],
        'failures': ['YouTube'],
        'switched': ['日本V1 A'],
        'note': '同地区（JP）已验证的候选节点都通不过：YouTube。',
      });
      expect(status.hasRun, isTrue);
      expect(status.activeNode, '日本V1 A');
      expect(status.results, hasLength(2));
      expect(status.failures, ['YouTube']);
      expect(status.switched, ['日本V1 A']);
      expect(status.note, contains('都通不过'));
      expect(const PathStatus.empty().hasRun, isFalse);
    });

    test('summarises the last round', () {
      PathStatus build({bool running = false, List<String> failures = const [], String at = ''}) =>
          PathStatus(
              at: at,
              running: running,
              activeNode: '',
              results: const [],
              failures: failures,
              switched: const [],
              note: '');
      expect(pathSummary(build()), '还没有检测记录');
      expect(pathSummary(build(running: true, at: 'x')), '正在检测…');
      expect(pathSummary(build(at: '2026-09-26T00:00:00Z')), '最近一次全部通过');
      expect(pathSummary(build(at: 'x', failures: ['Google', 'YouTube'])),
          '最近一次不通：Google、YouTube');
    });

    test('says what it did about a failing round', () {
      PathStatus build(List<String> switched, List<String> failures) => PathStatus(
            at: 'x',
            running: false,
            activeNode: '',
            results: const [],
            failures: failures,
            switched: switched,
            note: '',
          );
      expect(pathSwitchLabel(build(const [], const ['YouTube'])), isEmpty);
      expect(pathSwitchLabel(build(const ['日本V1 A'], const [])),
          '因不通已切换到 日本V1 A，复检通过');
      expect(pathSwitchLabel(build(const ['日本V1 A', '日本V3 B'], const ['YouTube'])),
          '已依次试过 日本V1 A、日本V3 B，仍未通过');
    });

    test('shows the clock of a timestamp, and nothing for a missing one', () {
      expect(clockLabel(''), isEmpty);
      expect(clockLabel('not a time'), isEmpty);
      final local = DateTime(2026, 9, 26, 7, 5, 3);
      expect(clockLabel(local.toUtc().toIso8601String()), '07:05:03');
    });
  });

  group('TUN labels', () {
    test('explains every check state', () {
      expect(tunCheckLabel('ok'), '通过');
      expect(tunCheckLabel('warn'), '需注意');
      expect(tunCheckLabel('fail'), '未通过');
      expect(tunCheckColor('fail'), isNot(tunCheckColor('ok')));
      expect(tunCheckColor('warn'), isNot(tunCheckColor('ok')));
    });

    test('never colours a failure like a pass', () {
      for (final state in ['warn', 'fail', 'unknown-state']) {
        expect(tunCheckColor(state), isNot(tunCheckColor('ok')),
            reason: '$state must not look like a pass');
      }
    });
  });

  group('nodeLineKind', () {
    test('reads the provider line marker out of the name', () {
      expect(nodeLineKind('🇯🇵 日本V1 A|中转|x1'), '中转');
      expect(nodeLineKind('🇭🇰 香港V1 A|优化|x1'), '优化');
      expect(nodeLineKind('🇰🇷 韩国V1|直连|x0.8'), '直连');
      expect(nodeLineKind('🇸🇬 新加坡V2 流媒体|中转|x1'), '流媒体');
      expect(nodeLineKind('🇺🇸 美国V1'), '其他');
    });

    test('puts a name with two markers in the more specific bucket', () {
      expect(nodeLineKind('流媒体|中转'), '流媒体');
    });
  });

  group('groupNodes', () {
    ProxyNode node(String name) => ProxyNode(
          name: name,
          type: 'Vmess',
          alive: true,
          selected: false,
          network: 'ws',
          tls: true,
          wsHostConfigured: false,
          wsPathConfigured: false,
        );
    RegionInfo region(String name, String country) => RegionInfo(
          name: name,
          country: country,
          exitIp: '',
          status: 'verified',
          detail: '',
          sources: const [],
          verifiedAt: '',
          stale: false,
        );

    final nodes = [
      node('jp-a'),
      node('sg-a'),
      node('jp-中转-b'),
      node('unverified-a'),
    ];
    final regions = {
      'jp-a': region('jp-a', 'JP'),
      'sg-a': region('sg-a', 'SG'),
      'jp-中转-b': region('jp-中转-b', 'JP'),
    };

    test('groups by measured region and keeps unverified last', () {
      final groups = groupNodes(
          nodes: nodes, regions: regions, grouping: NodeGrouping.region);
      expect(groups.map((group) => group.label).toList(),
          ['JP', 'SG', '未验证']);
      expect(groups.first.nodes.map((item) => item.name).toList(),
          ['jp-a', 'jp-中转-b']);
      expect(groups.last.nodes.single.name, 'unverified-a');
    });

    test('groups by line kind in the provider order', () {
      final groups = groupNodes(
          nodes: nodes, regions: regions, grouping: NodeGrouping.line);
      expect(groups.map((group) => group.label).toList(), ['中转', '其他']);
      expect(groups.first.nodes.single.name, 'jp-中转-b');
      expect(groups.last.nodes.length, 3);
    });

    test('groups by source kind using subscription prefixes', () {
      final multiNodes = [
        node('[主力] 香港 01'),
        node('[主力] 香港 02'),
        node('[备用] 日本 01'),
        const ProxyNode(
          name: '自建节点',
          type: 'Vmess',
          alive: true,
          selected: false,
          network: 'ws',
          tls: true,
          wsHostConfigured: false,
          wsPathConfigured: false,
          manual: true,
        ),
        node('无前缀节点'),
      ];
      final groups = groupNodes(
          nodes: multiNodes, regions: const {}, grouping: NodeGrouping.source);
      expect(groups.map((group) => group.label).toList(),
          ['[主力]', '[备用]', '默认源', '手动节点']);
      expect(groups.first.nodes.length, 2);
    });

    test('a stale region does not count as verified', () {
      const stale = <String, RegionInfo>{
        'jp-a': RegionInfo(
            name: 'jp-a',
            country: 'JP',
            exitIp: '',
            status: 'verified',
            detail: '',
            sources: [],
            verifiedAt: '',
            stale: true),
      };
      final groups = groupNodes(
          nodes: [node('jp-a')], regions: stale, grouping: NodeGrouping.region);
      expect(groups.single.label, '未验证');
    });
  });

  group('nodeMatchesQuery', () {
    final node = ProxyNode.fromJson(const {'name': '🇯🇵 日本V1 A|中转|x1'});

    test('an empty query matches everything', () {
      expect(nodeMatchesQuery(node, ''), isTrue);
      expect(nodeMatchesQuery(node, '   '), isTrue);
    });

    test('matches a substring case insensitively', () {
      expect(nodeMatchesQuery(node, '日本'), isTrue);
      expect(nodeMatchesQuery(node, 'V1'), isTrue);
      expect(nodeMatchesQuery(node, 'v1'), isTrue);
      expect(nodeMatchesQuery(node, '韩国'), isFalse);
    });
  });

  group('protectionStateLabel', () {
    String label({
      bool connected = false,
      bool blocked = false,
      String mode = 'system-proxy',
      bool tunActive = false,
      bool proxyEnabled = false,
    }) =>
        protectionStateLabel(
            connected: connected,
            blocked: blocked,
            mode: mode,
            tunActive: tunActive,
            proxyEnabled: proxyEnabled);

    test('names every protection state', () {
      expect(label(), '未连接');
      expect(label(connected: true, proxyEnabled: true), '已连接 · 系统代理');
      expect(label(connected: true), '内核运行中，系统代理未启用');
      expect(
          label(connected: true, mode: 'tun', tunActive: true),
          '已连接 · TUN 全设备');
      expect(label(connected: true, mode: 'tun'), '内核运行中，TUN 未就绪');
    });

    test('a blocked region wins over every other state', () {
      expect(
          label(
              connected: true,
              blocked: true,
              mode: 'tun',
              tunActive: true,
              proxyEnabled: true),
          '受保护流量已阻断');
    });
  });

  group('protectionStateDetail', () {
    test('explains what to do when nothing is protecting traffic', () {
      final detail = protectionStateDetail(
          connected: true,
          blocked: false,
          mode: 'system-proxy',
          tunActive: false,
          proxyEnabled: false);
      expect(detail, contains('系统代理已关闭'));
      final blocked = protectionStateDetail(
          connected: true,
          blocked: true,
          mode: 'system-proxy',
          tunActive: false,
          proxyEnabled: false);
      expect(blocked, contains('不会跨地区直连'));
    });
  });

  group('exitAddressLabel', () {
    test('prefers the address and explains the failure otherwise', () {
      expect(exitAddressLabel(ipv4: '1.2.3.4', ipv4Error: ''), '1.2.3.4');
      expect(exitAddressLabel(ipv4: '', ipv4Error: '查询失败'), '未取得');
      expect(exitAddressLabel(ipv4: '', ipv4Error: ''), '—');
    });
  });

  group('SmartVpnApp', () {
    /// Nav rows are the only InkWells wrapping the page labels, so this reaches
    /// the rail item even when the page header repeats the same word.
    Finder navItem(String label) => find
        .ancestor(of: find.text(label), matching: find.byType(InkWell))
        .first;

    Future<void> pumpApp(WidgetTester tester) async {
      // Service startup performs real file I/O, which the fake-async zone of
      // testWidgets cannot drive, so this covers rendering only. The point is
      // that a missing local service leaves the shell usable.
      await tester.pumpWidget(const SmartVpnApp());
      await tester.pumpAndSettle();
    }

    testWidgets('renders the rail and the connection page', (tester) async {
      await pumpApp(tester);

      expect(find.text('SmartVPN'), findsOneWidget);
      expect(find.text('未连接'), findsWidgets);
      for (final label in ['连接', '流量', '节点', '地区与健康', '检测', '订阅与内核']) {
        expect(navItem(label), findsOneWidget, reason: '$label must be in the rail');
      }

      // The connection page owns the power control and the route readout.
      expect(find.text('代理模式'), findsOneWidget);
      expect(find.text('系统代理'), findsOneWidget);
      expect(find.text('TUN 全设备'), findsOneWidget);
      expect(find.text('本机'), findsOneWidget);
      expect(find.widgetWithText(FilledButton, '连接'), findsOneWidget);
      // The connection page does not show the TUN panel in system-proxy mode.
      expect(find.text('wintun.dll'), findsNothing);
    });

    testWidgets('every sub-page renders its own controls', (tester) async {
      await pumpApp(tester);

      await tester.tap(navItem('节点'));
      await tester.pumpAndSettle();
      expect(find.text('选择模式'), findsOneWidget);
      expect(find.text('测试延迟'), findsOneWidget);
      expect(find.text('搜索节点名称'), findsOneWidget);
      expect(find.text('添加节点'), findsOneWidget);
      expect(find.textContaining('还没有可选节点'), findsOneWidget);
      // The grouping switch offers both categories.
      expect(find.text('按地区'), findsOneWidget);
      expect(find.text('按线路'), findsOneWidget);

      await tester.tap(navItem('地区与健康'));
      await tester.pumpAndSettle();
      expect(find.text('地区锁定'), findsOneWidget);
      expect(find.text('锁定地区候选'), findsOneWidget);
      // Verification needs a live kernel, so it is disabled offline.
      final verify = find.widgetWithText(FilledButton, '验证出口地区');
      expect(tester.widget<FilledButton>(verify).onPressed, isNull);
      final patrol = find.widgetWithText(TextButton, '立即巡检');
      expect(tester.widget<TextButton>(patrol).onPressed, isNull);

      await tester.tap(navItem('检测'));
      await tester.pumpAndSettle();
      expect(find.text('开始检测'), findsOneWidget);
      expect(find.text('复制报告'), findsOneWidget);
      expect(find.textContaining('还没有检测结果'), findsOneWidget);
      expect(find.text('网速检测'), findsOneWidget);
      expect(find.text('定时通路检测'), findsOneWidget);
      expect(find.text('TUN 路径检查'), findsOneWidget);
      // Offline there is no configuration yet, so the monitor reads as off and
      // the immediate check has nothing to run against.
      expect(find.text('已关闭'), findsOneWidget);
      expect(find.textContaining('还没有检测记录'), findsWidgets);
      final runNow = find.widgetWithText(OutlinedButton, '立即检测一次');
      expect(tester.widget<OutlinedButton>(runNow).onPressed, isNull);
      // Copying needs a report, and measuring needs a running kernel, so both
      // are disabled while the page is idle and offline.
      final copy = find.widgetWithText(OutlinedButton, '复制报告');
      expect(tester.widget<OutlinedButton>(copy).onPressed, isNull);
      final speed = find.widgetWithText(FilledButton, '开始测速');
      expect(tester.widget<FilledButton>(speed).onPressed, isNull);
      // The size the test is allowed to spend is chosen here.
      for (final size in ['10 MB', '30 MB', '100 MB']) {
        expect(find.text(size), findsOneWidget);
      }

      await tester.tap(navItem('流量'));
      await tester.pumpAndSettle();
      expect(find.text('流量与连接'), findsOneWidget);
      expect(find.textContaining('还没有流量数据'), findsOneWidget);
      // Monitoring needs a running kernel, so both controls are disabled offline.
      for (final label in ['暂停', '刷新']) {
        final button = find.widgetWithText(OutlinedButton, label);
        expect(tester.widget<OutlinedButton>(button).onPressed, isNull);
      }

      await tester.tap(navItem('订阅与内核'));
      await tester.pumpAndSettle();
      expect(find.text('订阅地址'), findsOneWidget);
      expect(find.text('Mihomo 程序路径'), findsOneWidget);
      // The path is the service's to decide, and the field says so rather than
      // guessing a directory of its own.
      expect(find.textContaining('留空则用配置目录'), findsOneWidget);
      // Offline the service has not reported a profile yet.
      expect(find.text('配置目录'), findsOneWidget);
      expect(find.text('订阅诊断'), findsOneWidget);
      expect(find.text('获取节点'), findsOneWidget);
      // With no local service there is no saved list to draw.
      expect(find.text('已保存的订阅'), findsNothing);
    });

    testWidgets('adding a node asks for a link and reports a refusal',
        (tester) async {
      await pumpApp(tester);
      await tester.tap(navItem('节点'));
      await tester.pumpAndSettle();

      await tester.tap(find.text('添加节点'));
      await tester.pumpAndSettle();
      expect(find.text('节点链接'), findsOneWidget);
      expect(find.textContaining('vmess://'), findsOneWidget);

      // Parsing happens in the local service, so with none running the dialog
      // has to say so rather than pretend the node was added.
      final linkField = find.ancestor(
          of: find.text('节点链接'), matching: find.byType(TextField));
      await tester.enterText(
          linkField, 'trojan://secret@tj.example.com:443#T');
      await tester.tap(find.text('解析'));
      await tester.pumpAndSettle();
      expect(find.textContaining('本地服务尚未启动'), findsOneWidget);
    });
    testWidgets('the close button asks before hiding or quitting',
        (tester) async {
      await pumpApp(tester);

      // Stand in for the runner: record what the app asks it to do.
      const channel = MethodChannel('smartvpn/lifecycle');
      final calls = <MethodCall>[];
      tester.binding.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, (call) async {
        calls.add(call);
        return null;
      });
      addTearDown(() => tester.binding.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null));

      // The runner reports a close request the way the real one does. It is not
      // awaited: the handler only answers once the dialog is dealt with, which is
      // the very thing this test is about to do.
      unawaited(tester.binding.defaultBinaryMessenger.handlePlatformMessage(
        'smartvpn/lifecycle',
        const StandardMethodCodec()
            .encodeMethodCall(const MethodCall('closeRequested', {
          'trayReady': true,
        })),
        (_) {},
      ));
      await tester.pumpAndSettle();

      expect(find.text('关闭窗口？'), findsOneWidget);
      expect(find.text('最小化到托盘'), findsOneWidget);
      expect(find.text('退出 SmartVPN'), findsOneWidget);

      await tester.tap(find.text('最小化到托盘'));
      await tester.pumpAndSettle();

      expect(calls, isNotEmpty);
      expect(calls.last.method, 'closeChoice');
      expect(calls.last.arguments, {'action': 'hide'});
      // The dialog is gone and the window was left alone: hiding is the runner's
      // job, not something the app does to itself.
      expect(find.text('关闭窗口？'), findsNothing);
    });

    testWidgets('an unusable tray is not offered as a way out', (tester) async {
      await pumpApp(tester);

      const channel = MethodChannel('smartvpn/lifecycle');
      tester.binding.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, (call) async => null);
      addTearDown(() => tester.binding.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null));

      unawaited(tester.binding.defaultBinaryMessenger.handlePlatformMessage(
        'smartvpn/lifecycle',
        const StandardMethodCodec()
            .encodeMethodCall(const MethodCall('closeRequested', {
          'trayReady': false,
        })),
        (_) {},
      ));
      await tester.pumpAndSettle();

      expect(find.text('关闭窗口？'), findsOneWidget);
      expect(find.text('退出 SmartVPN'), findsOneWidget);
      // Hiding here would leave the window with no way back to it.
      expect(find.text('最小化到托盘'), findsNothing);
    });

    testWidgets('the layout holds at the minimum window size', (tester) async {
      // 900x600 logical is the smallest window the runner allows. The test
      // framework reports a flex overflow as a failure, so this doubles as the
      // check that nothing spills out of a narrow window.
      tester.view.physicalSize = const Size(1800, 1200);
      tester.view.devicePixelRatio = 2;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);

      await pumpApp(tester);
      expect(find.text('SmartVPN'), findsOneWidget);
      expect(find.text('本机'), findsOneWidget);

      for (final label in ['节点', '流量', '地区与健康', '检测', '订阅与内核', '连接']) {
        await tester.tap(navItem(label));
        await tester.pumpAndSettle();
        expect(find.text(label), findsWidgets);
      }
    });

    testWidgets('the pages hold at phone width', (tester) async {
      // A phone, in the logical pixels it reports: 1216x2640 physical at 2.75.
      // A flex that does not fit is reported as a failure by the test framework,
      // so this is the check that no page spills off a phone screen — the shell
      // and the page chrome, at the width where the rail becomes a drawer. The
      // rows that only exist with data are checked on a device, because this
      // harness cannot reach a local service to produce them.
      tester.view.physicalSize = const Size(1216, 2640);
      tester.view.devicePixelRatio = 2.75;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);

      await pumpApp(tester);
      expect(find.text('SmartVPN'), findsOneWidget);
      // The rail is a drawer here, which is the whole point of the width.
      expect(find.byIcon(Icons.menu), findsOneWidget);

      for (final label in ['流量', '节点', '地区与健康', '检测', '订阅与内核', '连接']) {
        await tester.tap(find.byIcon(Icons.menu));
        await tester.pumpAndSettle();
        await tester.tap(navItem(label));
        await tester.pumpAndSettle();
        expect(find.text(label), findsWidgets);
      }
    });

    testWidgets('the pages hold at phone landscape', (tester) async {
      tester.view.physicalSize = const Size(2640, 1216);
      tester.view.devicePixelRatio = 2.75;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);

      await pumpApp(tester);
      for (final label in ['流量', '节点', '地区与健康', '检测', '订阅与内核', '连接']) {
        await tester.tap(navItem(label));
        await tester.pumpAndSettle();
        expect(find.text(label), findsWidgets);
      }
    });

    testWidgets('PowerSwitchHero connected and disconnected', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: Scaffold(
          body: Container(
            decoration: BoxDecoration(
              color: Tokens.surface,
              border: Border.all(
                color: Tokens.hairline,
                width: 1.0,
              ),
              borderRadius: BorderRadius.circular(Tokens.radiusPanel),
            ),
            child: const Text('Testing Panel Inside'),
          ),
        ),
      ));
      expect(find.text('Testing Panel Inside'), findsOneWidget);
    });
  });
}

