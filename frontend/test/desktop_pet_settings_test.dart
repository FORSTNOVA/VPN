import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/main.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  const channel = MethodChannel('smartvpn/lifecycle');

  for (final size in [const Size(900, 600), const Size(1440, 900)]) {
    testWidgets('pet settings fit $size and cancel preserves preferences',
        (tester) async {
      tester.view.physicalSize = size;
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final configured = <MethodCall>[];
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(channel,
          (call) async {
        if (call.method == 'getPetSettings') {
          return {
            'enabled': true,
            'scale': 1.0,
            'autoMiniOnFullscreen': true,
            'width': 360,
            'height': 82
          };
        }
        if (call.method == 'configurePet') configured.add(call);
        return null;
      });
      addTearDown(() => tester.binding.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null));
      await tester.pumpWidget(const SmartVpnApp());
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('桌宠设置'));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      final slider = find.byType(Slider).first;
      await tester.ensureVisible(slider);
      await tester.tapAt(tester.getTopLeft(slider) +
          Offset(tester.getSize(slider).width * 0.7, 24));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
      await tester.tap(find.text('取消'));
      await tester.pumpAndSettle();
      expect(configured, isEmpty);
      await tester.tap(find.byTooltip('桌宠设置'));
      await tester.pumpAndSettle();
      expect(tester.widget<Slider>(find.byType(Slider).first).value, 1.0);
      await tester.tap(find.byType(FilledButton).last);
      await tester.pumpAndSettle();
      expect(configured, hasLength(1));
      expect(configured.single.arguments, {
        'enabled': true,
        'scale': 1.0,
        'autoMiniOnFullscreen': true,
        'width': 360,
        'height': 82,
      });
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }
}
