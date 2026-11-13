import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/chain_dialog.dart';

void main() {
  testWidgets('two available nodes can swap entrance and exit on a phone',
      (tester) async {
    tester.view.physicalSize = const Size(360, 640);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    (bool, List<String>)? saved;
    await tester.pumpWidget(MaterialApp(home: Builder(builder: (context) {
      return Scaffold(
          body: TextButton(
        onPressed: () async {
          saved = await showDialog<(bool, List<String>)>(
            context: context,
            builder: (_) => const ChainDialog(
              available: ['A', 'B'],
              initialHops: ['A', 'B'],
              initialEnabled: true,
            ),
          );
        },
        child: const Text('设置'),
      ));
    })));
    await tester.tap(find.text('设置'));
    await tester.pumpAndSettle();
    await tester.tap(find.byType(DropdownButtonFormField<String>).first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('B').last);
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('保存'));
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved?.$1, isTrue);
    expect(saved?.$2, ['B', 'A']);
    expect(tester.takeException(), isNull);
  });
}
