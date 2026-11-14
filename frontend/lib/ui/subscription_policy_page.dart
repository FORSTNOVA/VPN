import 'package:flutter/material.dart';

class SubscriptionPolicyPage extends StatefulWidget {
  const SubscriptionPolicyPage(
      {super.key, required this.data, required this.toggle});
  final Map<String, dynamic> data;
  final Future<void> Function(bool) toggle;
  @override
  State<SubscriptionPolicyPage> createState() => _PolicyPageState();
}

class _PolicyPageState extends State<SubscriptionPolicyPage> {
  late bool enabled = widget.data['enabled'] == true;
  bool saving = false;
  int? selectedSource;
  Future<void> toggle(bool next) async {
    setState(() => saving = true);
    try {
      await widget.toggle(next);
      if (mounted) setState(() => enabled = next);
    } catch (e) {
      if (mounted)
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(e.toString())));
    } finally {
      if (mounted) setState(() => saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final allGroups =
        (widget.data['groups'] as List? ?? []).cast<Map<String, dynamic>>();
    final allRules =
        (widget.data['rules'] as List? ?? []).cast<Map<String, dynamic>>();
    final sections =
        (widget.data['sections'] as List? ?? []).cast<Map<String, dynamic>>();
    final section = selectedSource == null ? null : sections[selectedSource!];
    final groups = section == null
        ? allGroups
        : (section['groups'] as List? ?? []).cast<Map<String, dynamic>>();
    final originals = (section?['rules'] as List? ?? []);
    final start = sections
        .take(selectedSource ?? 0)
        .fold<int>(0, (n, s) => n + (s['rules'] as List? ?? []).length);
    final rules = section == null
        ? allRules
        : [
            for (var i = 0; i < originals.length; i++)
              <String, dynamic>{
                if (start + i < allRules.length) ...allRules[start + i],
                'source': originals[i]
              }
          ];
    final supported = allRules.where((r) => r['applied'] != null).length;
    return DefaultTabController(
        length: 2,
        child: Scaffold(
            appBar: AppBar(
                title: sections.isEmpty
                    ? const Text('订阅分组与分流')
                    : DropdownButton<int>(
                        isExpanded: true,
                        value: selectedSource ?? -1,
                        items: [
                          const DropdownMenuItem(
                              value: -1, child: Text('全部订阅')),
                          for (var i = 0; i < sections.length; i++)
                            DropdownMenuItem(
                                value: i,
                                child: Text(sections[i]['label'] as String,
                                    overflow: TextOverflow.ellipsis))
                        ],
                        onChanged: (i) => setState(() =>
                            selectedSource = i == null || i < 0 ? null : i)),
                bottom: TabBar(tabs: [
                  Tab(text: '分组 ${groups.length}'),
                  Tab(text: '规则 ${rules.length}')
                ])),
            body: TabBarView(children: [
              groups.isEmpty
                  ? const Center(child: Text('当前缓存没有内置分组。旧版合并订阅可先重新合并更新。'))
                  : ListView.builder(
                      itemCount: groups.length,
                      itemBuilder: (_, i) {
                        final g = groups[i];
                        final members = [
                          ...?g['members'] as List?,
                          ...?g['providers'] as List?
                        ];
                        return ExpansionTile(
                            key: PageStorageKey('group-$selectedSource-$i'),
                            title: Text(g['name'] as String),
                            subtitle:
                                Text('${g['type']} · ${members.length} 个显式成员'),
                            children: [
                              if (g['includeAll'] == true)
                                const ListTile(title: Text('包含全部节点')),
                              for (final m in members)
                                ListTile(dense: true, title: Text(m.toString()))
                            ]);
                      }),
              ListView.builder(
                  itemCount: rules.length + 1,
                  itemBuilder: (_, i) {
                    if (i == 0)
                      return SwitchListTile(
                          value: enabled,
                          onChanged: saving ||
                                  widget.data['running'] == true ||
                                  (supported == 0 && !enabled)
                              ? null
                              : toggle,
                          title: const Text('使用可解析的订阅分流'),
                          subtitle: Text(widget.data['running'] == true
                              ? '断开后可修改，下次连接生效'
                              : '可用 $supported 条。关闭时沿用默认分流，纯代理组使用当前出口。本机保护策略优先。'));
                    final r = rules[i - 1];
                    return ListTile(
                        dense: true,
                        title: Text(r['source'] as String),
                        subtitle: Text(r['applied'] == null
                            ? '仅供查看'
                            : '可应用 · ${r['applied']}'));
                  }),
            ])));
  }
}
