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
              return {'hops': prefix, 'delay': 123, 'warmDelay': 31, 'measured': 0};
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
}
