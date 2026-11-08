import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/game_bypass_dialog.dart';

void main() {
  for (final android in [false, true]) {
    testWidgets(
        'picker preserves existing edits and saves once (android=$android)',
        (tester) async {
      final channel =
          MethodChannel(android ? 'smartvpn/host' : 'smartvpn/lifecycle');
      final selection =
          android ? 'com.example.game' : r'D:\Games (x86)\游戏\Game.exe';
      final responses = <String?>[
        null,
        selection,
        android ? selection : selection.toUpperCase()
      ];
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(channel,
          (call) async {
        expect(call.method, 'pickGame');
        return responses.removeAt(0);
      });
      addTearDown(() => tester.binding.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null));
      List<String>? saved;
      await tester.pumpWidget(
          MaterialApp(home: Scaffold(body: Builder(builder: (context) {
        return TextButton(
            onPressed: () async {
              saved = await editGameBypass(context, [], android);
            },
            child: const Text('编辑'));
      }))));
      await tester.tap(find.text('编辑'));
      await tester.pumpAndSettle();
      final manual = android ? 'com.manual.game' : 'Manual.exe';
      await tester.enterText(find.byType(TextField), manual);
      final choose = find.byType(OutlinedButton);
      for (var i = 0; i < 3; i++) {
        await tester.tap(choose);
        await tester.pumpAndSettle();
      }
      expect(saved, isNull);
      await tester.tap(find.text('保存'));
      await tester.pumpAndSettle();
      expect(saved, [manual, selection]);
    });
  }
}
