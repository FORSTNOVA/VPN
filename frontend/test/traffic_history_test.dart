import 'package:flutter_test/flutter_test.dart';
import 'package:flutter/material.dart';
import 'package:smartvpn_windows/main.dart' show SmartVpnApp;
import 'package:smartvpn_windows/models.dart';

void main() {
  testWidgets('phone history controls work without an active VPN',
      (tester) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(const SmartVpnApp());
    await tester.pumpAndSettle();
    await tester.tap(find.descendant(
        of: find.byType(NavigationBar), matching: find.text('流量')));
    await tester.pumpAndSettle();
    expect(find.text('历史流量统计'), findsOneWidget);
    for (final period in [
      TrafficHistoryPeriod.day,
      TrafficHistoryPeriod.month,
      TrafficHistoryPeriod.year,
      TrafficHistoryPeriod.hour
    ]) {
      await tester.tap(find.text(period.label));
      await tester.pumpAndSettle();
      final selector = tester.widget<SegmentedButton<TrafficHistoryPeriod>>(
          find.byType(SegmentedButton<TrafficHistoryPeriod>));
      expect(selector.selected, {period});
      expect(tester.takeException(), isNull);
    }
    await tester.tap(find.byTooltip('选择时段（0-23时）'));
    await tester.pumpAndSettle();
    final midnight = find.text('00:00 - 00:59').last;
    await tester.ensureVisible(midnight);
    await tester.tap(midnight);
    await tester.pumpAndSettle();
    expect(find.text('00:00 - 00:59'), findsOneWidget);
    await tester.tap(find.byTooltip('上一时段'));
    await tester.pumpAndSettle();
    expect(find.text('23:00 - 23:59'), findsOneWidget);
    await tester.tap(find.text('连接明细'));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
  });

  test('history preserves full period totals when the page contains fewer rows',
      () {
    final report = TrafficHistoryResponse.fromJson({
      'period': 'hour',
      'date': '2026-11-05T10',
      'group': 'connection',
      'uploadTotal': 9000000000,
      'downloadTotal': 12000000000,
      'connectionCount': 135,
      'rowCount': 135,
      'hasMore': true,
      'rows': [
        {
          'id': 'connection-1',
          'app': '',
          'appKey': 'unknown',
          'upload': 100,
          'download': 200,
          'connections': 1,
          'target': 'example.com:443',
          'chain': 'DIRECT',
          'network': 'tcp',
          'firstSeen': 1700000000,
          'lastSeen': 1700000001
        }
      ],
    });
    expect(report.period, TrafficHistoryPeriod.hour);
    expect(report.uploadTotal, 9000000000);
    expect(report.downloadTotal, 12000000000);
    expect(report.rowCount, 135);
    expect(report.rows.single.displayAppName, '未识别应用');
    expect(report.rows.single.target, 'example.com:443');
    expect(report.hasMore, isTrue);
  });

  test('calendar date formats cover hour day month year', () {
    final date = DateTime(2024, 2, 29, 23);
    final expected = ['2024-02-29T23', '2024-02-29', '2024-02', '2024'];
    for (final period in TrafficHistoryPeriod.values) {
      final text =
          formatTrafficHistoryDate(period: period, date: date, hour: 23);
      expect(text, expected[period.index]);
    }
  });

  test('application filters are encoded without changing pagination or date',
      () {
    const app = r'path:C:\Apps\A & B\应用.exe';
    final uri = Uri.parse(buildTrafficHistoryRoute(
      period: TrafficHistoryPeriod.hour,
      date: '2026-11-05T10',
      group: TrafficHistoryGroup.connection,
      appKey: app,
      offset: 100,
    ));
    expect(uri.queryParameters['app'], app);
    expect(uri.queryParameters['date'], '2026-11-05T10');
    expect(uri.queryParameters['group'], 'connection');
    expect(uri.queryParameters['offset'], '100');
  });
}
