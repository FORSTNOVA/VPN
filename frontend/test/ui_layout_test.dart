import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/main.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  for (final size in [
    const Size(390, 844),
    const Size(800, 900),
    const Size(1440, 900)
  ]) {
    testWidgets('all pages fit ${size.width} x ${size.height}', (tester) async {
      tester.view.physicalSize = size;
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      await tester.pumpWidget(const SmartVpnApp());
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      for (final label in ['线路', '订阅', '流量', '保护策略', '工具']) {
        if (size.width < 700) {
          if (label == '保护策略' || label == '工具') {
            await tester.tap(find.text('更多'));
            await tester.pumpAndSettle();
            await tester.tap(find.text(label).last);
          } else {
            await tester.tap(find.descendant(
                of: find.byType(NavigationBar), matching: find.text(label)));
          }
        } else {
          await tester.tap(find
              .ancestor(of: find.text(label), matching: find.byType(InkWell))
              .first);
        }
        await tester.pumpAndSettle();
        expect(tester.takeException(), isNull);
      }
    });
  }
}
