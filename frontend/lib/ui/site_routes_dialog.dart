import 'dart:io';

import 'package:flutter/material.dart';

/// Return only the routing host. Paths, query tokens and fragments stay local.
String siteHost(String input) {
  final text = input.trim();
  if (text.isEmpty || RegExp(r'[\s\\]').hasMatch(text)) {
    throw const FormatException('请输入网址或域名');
  }
  final uri = Uri.tryParse(
    RegExp(r'^[a-zA-Z][a-zA-Z0-9+.-]*://').hasMatch(text)
        ? text
        : 'https://$text',
  );
  if (uri == null ||
      !{'http', 'https'}.contains(uri.scheme) ||
      uri.userInfo.isNotEmpty ||
      uri.host.isEmpty) {
    throw const FormatException('请输入 HTTP、HTTPS 网址或域名');
  }
  final host = uri.host.toLowerCase().replaceFirst(RegExp(r'\.$'), '');
  final ip = InternetAddress.tryParse(host);
  if (ip != null) return ip.address;
  final label = RegExp(r'^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$');
  if (host.length > 253 ||
      !host.contains('.') ||
      RegExp(r'^[\d.]+$').hasMatch(host) ||
      !host.split('.').every(label.hasMatch)) {
    throw const FormatException('请输入有效域名；国际化域名可使用 xn-- 形式');
  }
  return host;
}

Future<List<Map<String, dynamic>>?> editSiteRoutes(
  BuildContext context,
  List<Map<String, dynamic>> rules,
) {
  return showDialog<List<Map<String, dynamic>>>(
    context: context,
    builder: (_) => _SiteRoutesDialog(rules),
  );
}

class _SiteRoutesDialog extends StatefulWidget {
  const _SiteRoutesDialog(this.rules);
  final List<Map<String, dynamic>> rules;
  @override
  State<_SiteRoutesDialog> createState() => _SiteRoutesDialogState();
}

class _SiteRoutesDialogState extends State<_SiteRoutesDialog> {
  final input = TextEditingController();
  late final rules = [...widget.rules];
  var action = 'direct';
  var subdomains = true;
  String? error;

  @override
  void dispose() {
    input.dispose();
    super.dispose();
  }

  void addRule() {
    try {
      final host = siteHost(input.text);
      final entry = {
        'host': host,
        'action': action,
        'includeSubdomains':
            subdomains && InternetAddress.tryParse(host) == null,
      };
      setState(() {
        rules.removeWhere((r) => r['host'] == host);
        rules.add(entry);
        input.clear();
        error = null;
      });
    } on FormatException catch (e) {
      setState(() => error = e.message);
    }
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: const Text('网址分流'),
    content: SizedBox(
      width: 460,
      child: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('同一域名下的所有页面共用规则。外部链接、第三方视频和 CDN 按各自域名匹配。'),
            TextField(
              controller: input,
              keyboardType: TextInputType.url,
              onChanged: (_) => setState(() => error = null),
              decoration: InputDecoration(
                labelText: '网址或域名',
                hintText: 'https://example.com/page',
                errorText: error,
              ),
            ),
            const SizedBox(height: 8),
            SegmentedButton<String>(
              segments: const [
                ButtonSegment(value: 'direct', label: Text('直连')),
                ButtonSegment(value: 'proxy', label: Text('走代理')),
              ],
              selected: {action},
              onSelectionChanged: (s) => setState(() => action = s.single),
            ),
            CheckboxListTile(
              contentPadding: EdgeInsets.zero,
              title: const Text('包含子域名'),
              value: subdomains,
              onChanged: (v) => setState(() => subdomains = v!),
            ),
            OutlinedButton(
              onPressed: input.text.trim().isEmpty ? null : addRule,
              child: const Text('添加或更新'),
            ),
            for (final rule in rules)
              ListTile(
                contentPadding: EdgeInsets.zero,
                title: Text(rule['host'] as String),
                subtitle: Text(
                  '${rule['action'] == 'direct' ? '直连' : '走代理'} · ${rule['includeSubdomains'] == true ? '含子域名' : '仅此主机'}',
                ),
                trailing: IconButton(
                  tooltip: '删除规则',
                  icon: const Icon(Icons.delete_outline),
                  onPressed: () => setState(() => rules.remove(rule)),
                ),
              ),
          ],
        ),
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('取消'),
      ),
      FilledButton(
        onPressed: input.text.trim().isEmpty
            ? () => Navigator.pop(context, rules)
            : null,
        child: const Text('保存'),
      ),
    ],
  );
}
