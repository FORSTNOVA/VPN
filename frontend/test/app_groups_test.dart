import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/app_groups_dialog.dart';

void main() {
  final initial = <Map<String, dynamic>>[
    {
      'id': 'first',
      'name': '浏览器',
      'apps': ['chrome.exe'],
      'enabled': true,
      'targetType': 'direct'
    }
  ];

  Future<void> open(WidgetTester tester, bool android,
      ValueChanged<List<Map<String, dynamic>>?> done) async {
    await tester.pumpWidget(MaterialApp(
        home: Builder(
            builder: (context) => Scaffold(
                body: TextButton(
                    onPressed: () async => done(await editAppGroups(
                        context, initial, ['Japan'], android)),
                    child: const Text('打开'))))));
    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
  }

  Future<void> tap(WidgetTester tester, String label) async {
    await tester.tap(find.text(label).last);
    await tester.pumpAndSettle();
  }

  testWidgets('cancel discards switch and deletion', (tester) async {
    var cancelled = false;
    await open(tester, false, (v) => cancelled = v == null);
    await tester.tap(find.byType(Switch));
    await tester.tap(find.byTooltip('删除分组'));
    await tester.pumpAndSettle();
    expect(find.text('浏览器'), findsNothing);
    await tap(tester, '取消');
    expect(cancelled, isTrue);
    expect(initial.single['enabled'], true);
  });

  testWidgets('add group with a selected node and Windows executable',
      (tester) async {
    const channel = MethodChannel('smartvpn/lifecycle');
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(channel,
        (call) async {
      expect(call.method, 'pickGame');
      return r'C:\Programs\Gemini.exe';
    });
    addTearDown(() => tester.binding.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, null));
    List<Map<String, dynamic>>? saved;
    await open(tester, false, (v) => saved = v);
    await tap(tester, '添加分组');
    await tester.enterText(find.byType(TextFormField).first, ' Google ');
    await tap(tester, '确定');
    expect(find.text('请选择可用节点'), findsOneWidget);
    await tap(tester, '请选择节点');
    await tap(tester, 'Japan');
    await tap(tester, '选择应用');
    await tap(tester, '确定');
    await tap(tester, '保存');
    expect(saved!.last['name'], 'Google');
    expect(saved!.last['apps'], ['Gemini.exe']);
    expect(saved!.last['targetNode'], 'Japan');
    expect(saved!.last['targetType'], 'node');
    expect(saved!.last['enabled'], true);
  });

  testWidgets('edit group with Android package picker and reject target',
      (tester) async {
    const channel = MethodChannel('smartvpn/host');
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(channel,
        (call) async {
      expect(call.method, 'listGames');
      return [
        {'label': 'Example', 'package': 'com.example.app'}
      ];
    });
    addTearDown(() => tester.binding.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, null));
    List<Map<String, dynamic>>? saved;
    await open(tester, true, (v) => saved = v);
    await tap(tester, '浏览器');
    await tap(tester, '直连 (DIRECT)');
    await tap(tester, '拦截 (REJECT)');
    await tester.enterText(find.byType(TextFormField).last, '');
    await tap(tester, '选择应用');
    await tap(tester, 'Example');
    await tap(tester, '确定 (1)');
    await tap(tester, '确定');
    await tap(tester, '保存');
    expect(saved!.single, {
      ...initial.single,
      'targetType': 'reject',
      'apps': ['com.example.app']
    });
  });

  testWidgets('empty apps is rejected by group editor', (tester) async {
    List<Map<String, dynamic>>? saved;
    await open(tester, true, (v) => saved = v);
    await tap(tester, '添加分组');
    await tester.enterText(find.byType(TextFormField).first, 'Empty');
    await tap(tester, '指定节点');
    await tap(tester, '直连 (DIRECT)');
    await tap(tester, '确定');
    expect(find.text('请至少填写或选择一个应用'), findsOneWidget);
    expect(saved, isNull);
  });
}
