import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/installed_game_picker.dart';

void main() {
  testWidgets('installed app picker preserves selection across searches',
      (tester) async {
    List<String>? saved;
    await tester.pumpWidget(MaterialApp(
        home: Builder(
            builder: (context) => TextButton(
                onPressed: () async {
                  saved = await showDialog<List<String>>(
                      context: context,
                      builder: (_) => const InstalledGamePicker(apps: [
                            {'label': '星铁', 'package': 'org.game.rail'},
                            {'label': '崩坏', 'package': 'org.game.bh3'},
                          ], initial: [
                            'manual.game',
                            'org.game.bh3'
                          ]));
                },
                child: const Text('选择')))));
    Future<void> tap(String text) async {
      await tester.tap(find.text(text).last);
      await tester.pumpAndSettle();
    }

    await tap('选择');
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
    expect(saved, ['manual.game', 'org.game.rail']);
  });
}
