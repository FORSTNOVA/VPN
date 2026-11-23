import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/chain_editor.dart';

import 'package:smartvpn_windows/ui/probe_book.dart';

void main() {
  testWidgets('test candidate through entry before selecting and saving',
      (tester) async {
    List<String>? tested, saved;
    await tester.pumpWidget(MaterialApp(
        home: ChainEditor(
            available: const ['entry', 'exit'],
            initialHops: const ['entry'],
            initialName: 'test',
            book: ProbeBook((prefix) async {
              tested = prefix;
              return {
                'hops': prefix,
                'delay': 123,
                'warmDelay': 31,
                'measured': 0
              };
            }),
            save: (name, hops) async {
              saved = hops;
            })));
    await tester.tap(find.text('追加下一跳节点'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('测此路径').last);
    await tester.pumpAndSettle();
    expect(tested, ['entry', 'exit']);
    expect(find.text('测此路径 · 首次 123 ms · 连接后 31 ms'), findsOneWidget);
    await tester.tap(find.text('exit'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved, ['entry', 'exit']);
  });
  Future<void> openPicker(WidgetTester tester, {double width = 960}) async {
    await tester.binding.setSurfaceSize(Size(width, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(MaterialApp(
        home: ChainEditor(
      available: const [
        'entry',
        'manual',
        'plain',
        '[West] Tokyo',
        'Osaka',
        'Kyoto'
      ],
      initialHops: const ['entry'],
      initialName: 'chain',
      nodeGroups: const {
        'manual': '手动节点',
        '[West] Tokyo': ' ',
        'Osaka': 'East',
        'Kyoto': 'East'
      },
      book: ProbeBook((_) => throw StateError('Unexpected probe')),
      save: (_, __) async {},
    )));
    await tester.tap(find.text('追加下一跳节点'));
    await tester.pumpAndSettle();
  }

  Future<void> search(WidgetTester tester, String text) async {
    await tester.enterText(find.widgetWithText(TextField, '搜索节点名或订阅分组'), text);
    await tester.pumpAndSettle();
  }

  testWidgets('search matches node and source names and updates chip counts',
      (tester) async {
    await openPicker(tester);
    final chips = tester.widgetList<ChoiceChip>(find.byType(ChoiceChip));
    expect(chips.map((chip) => (chip.label as Text).data),
        ['全部 (5)', '[West] (1)', 'East (2)', '默认源 (1)', '手动节点 (1)']);
    await search(tester, ' oSa ');
    expect(find.text('Osaka'), findsOneWidget);
    expect(find.text('Kyoto'), findsNothing);
    expect(find.text('East (1)'), findsOneWidget);
    await search(tester, ' eAST ');
    expect(find.text('Kyoto'), findsOneWidget);
    expect(find.text('全部 (2)'), findsOneWidget);
    await tester.tap(find.byTooltip('清除搜索'));
    await tester.pumpAndSettle();
    expect(find.text('全部 (5)'), findsOneWidget);
  });

  testWidgets('group filter persists at zero matches and can be reset',
      (tester) async {
    await openPicker(tester);
    await tester.tap(find.text('East (2)'));
    await tester.pumpAndSettle();
    expect(find.text('Osaka'), findsOneWidget);
    expect(find.text('[West] Tokyo'), findsNothing);
    await search(tester, 'tokyo');
    expect(find.text('未找到匹配的节点'), findsOneWidget);
    expect(
        tester
            .widget<ChoiceChip>(find.widgetWithText(ChoiceChip, 'East (0)'))
            .selected,
        isTrue);
    await tester.tap(find.text('全部 (1)'));
    await tester.pumpAndSettle();
    expect(find.text('[West] Tokyo'), findsOneWidget);
    await tester.tap(find.text('East (0)'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('清除筛选'));
    await tester.pumpAndSettle();
    expect(
        tester
            .widget<ChoiceChip>(find.widgetWithText(ChoiceChip, '全部 (5)'))
            .selected,
        isTrue);
    expect(find.text('Osaka'), findsOneWidget);
  });

  testWidgets('search reveals collapsed groups and clearing restores collapse',
      (tester) async {
    await openPicker(tester);
    await tester.tap(find.text('[West]'));
    await tester.pumpAndSettle();
    expect(find.text('[West] Tokyo'), findsNothing);
    await search(tester, 'TOKYO');
    expect(find.text('[West] Tokyo'), findsOneWidget);
    await tester.tap(find.byTooltip('清除搜索'));
    await tester.pumpAndSettle();
    expect(find.text('[West] Tokyo'), findsNothing);
    await tester.tap(find.text('[West]'));
    await tester.pumpAndSettle();
    expect(find.text('[West] Tokyo'), findsOneWidget);
  });

  for (final width in [360.0, 960.0]) {
    testWidgets('group chips and probe actions fit at width $width',
        (tester) async {
      await openPicker(tester, width: width);
      final manual = find.text('手动节点 (1)');
      await tester.ensureVisible(manual);
      await tester.tap(manual);
      await tester.pumpAndSettle();
      final node = find.widgetWithText(ListTile, 'manual');
      expect(
          find.descendant(of: node, matching: find.text('测单节点')).hitTestable(),
          findsOneWidget);
      expect(
          find.descendant(of: node, matching: find.text('测此路径')).hitTestable(),
          findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  }
}
