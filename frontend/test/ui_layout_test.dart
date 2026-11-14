import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/main.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  testWidgets('Windows proxy port rejects out-of-range input before saving', (tester) async {
    tester.view.physicalSize = const Size(1440, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(const SmartVpnApp());
    await tester.pumpAndSettle();
    final section = find.text('代理模式与分流');
    await tester.ensureVisible(section);
    await tester.tap(section);
    await tester.pumpAndSettle();
    final row = find.text('SOCKS5 代理端口');
    await tester.ensureVisible(row);
    await tester.tap(row);
    await tester.pumpAndSettle();
    final dialog = find.byType(AlertDialog);
    await tester.enterText(find.descendant(of: dialog, matching: find.byType(TextFormField)), '65536');
    await tester.tap(find.descendant(of: dialog, matching: find.text('保存')));
    await tester.pump();
    expect(find.text('请输入 1–65535'), findsOneWidget);
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(dialog, findsNothing);
  }, variant: TargetPlatformVariant.only(TargetPlatform.windows), skip: !Platform.isWindows);
  testWidgets('phone keeps route actions in a dismissible sheet', (tester) async {
    tester.view.physicalSize=const Size(360,640);
    tester.view.devicePixelRatio=1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(const SmartVpnApp());
    await tester.pumpAndSettle();
    await tester.tap(find.descendant(of:find.byType(NavigationBar), matching:find.text('线路')));
    await tester.pumpAndSettle();
    expect(find.text('测全部节点'),findsNothing);
    await tester.tap(find.byTooltip('线路工具'));
    await tester.pumpAndSettle();
    expect(find.text('订阅分组与分流'),findsOneWidget);
    expect(find.text('测全部节点'),findsOneWidget);
    await tester.tap(find.text('按来源'));
    await tester.pumpAndSettle();
    expect(find.text('测全部节点'),findsNothing);
    expect(tester.takeException(),isNull);
  });
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
