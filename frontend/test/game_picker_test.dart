import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/game_bypass_dialog.dart';

void main() {
  testWidgets('Windows picker preserves existing edits and saves once',
      (tester) async {
    const channel = MethodChannel('smartvpn/lifecycle');
    const selection = r'D:\Games (x86)\游戏\Game.exe';
    final responses = <String?>[null, selection, selection.toUpperCase()];
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
            saved = await editGameBypass(context, [], false);
          },
          child: const Text('编辑'));
    }))));
    await tester.tap(find.text('编辑'));
    await tester.pumpAndSettle();
    const manual = 'Manual.exe';
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
  testWidgets('Android preserves checks across searches and cancellation',
      (tester) async {
    const chan = MethodChannel('smartvpn/host');
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(chan,
        (call) async {
      expect(call.method, 'listGames');
      return [
        {'label': '星铁', 'package': 'org.game.rail'},
        {'label': '崩坏', 'package': 'org.game.bh3'},
      ];
    });
    addTearDown(() => tester.binding.defaultBinaryMessenger
        .setMockMethodCallHandler(chan, null));
    List<String>? saved;
    await tester.pumpWidget(MaterialApp(
        home: Builder(
            builder: (context) => TextButton(
                onPressed: () async {
                  saved = await editGameBypass(
                      context, ['manual.game', 'org.game.bh3'], true);
                },
                child: const Text('编辑')))));
    Future<void> tap(String text) async {
      await tester.tap(find.text(text).last);
      await tester.pumpAndSettle();
    }

    await tap('编辑');
    await tap('选择已安装的游戏');
    final search = find.byType(TextField).last;
    await tester.enterText(search, '星铁');
    await tester.pump();
    await tap('星铁');
    await tester.enterText(search, 'BH3');
    await tester.pump();
    await tap('崩坏');
    await tester.enterText(search, 'missing.game');
    await tester.pump();
    expect(find.byType(CheckboxListTile), findsNothing);
    await tap('确定 (2)');
    await tap('选择已安装的游戏');
    await tap('崩坏');
    await tap('取消');
    await tap('保存');
    expect(saved, ['manual.game', 'org.game.rail']);
  });
}
