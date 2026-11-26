import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smartvpn_windows/ui/site_routes_dialog.dart';

void main() {
  test('keeps the requested host and drops path and query', () {
    expect(
      siteHost('https://WWW.Example.org:8443/a/b?token=test#foo'),
      'www.example.org',
    );
    expect(siteHost('example.org/path'), 'example.org');
    expect(siteHost('http://[::1]:8080/'), '::1');
    expect(
      () => siteHost('https://user:password@example.org'),
      throwsFormatException,
    );
    expect(() => siteHost('https://example.org,REJECT'), throwsFormatException);
  });

  testWidgets('pastes a page URL and saves a proxy rule for its domain',
      (tester) async {
    List<Map<String, dynamic>>? saved;
    await tester.pumpWidget(MaterialApp(
        home: Scaffold(
            body: Builder(
      builder: (context) => TextButton(
          onPressed: () async {
            saved = await editSiteRoutes(context, []);
          },
          child: const Text('打开')),
    ))));
    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.enterText(
        find.byType(TextField), 'https://www.example.org/watch?v=123');
    await tester.pumpAndSettle();
    await tester.tap(find.text('走代理'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('添加或更新'));
    await tester.pumpAndSettle();
    expect(find.text('www.example.org'), findsOneWidget);
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(saved, [
      {'host': 'www.example.org', 'action': 'proxy', 'includeSubdomains': true}
    ]);
  });
}
