import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'installed_game_picker.dart';

typedef _Group = Map<String, dynamic>;

Future<List<_Group>?> editAppGroups(BuildContext context, List<_Group> current,
    List<String> nodes, bool android,
    {Map<String, String>? chains}) {
  final draft = [...current];
  return showDialog<List<_Group>>(
    context: context,
    builder: (context) => StatefulBuilder(builder: (context, refresh) {
      Future<void> edit(int? index) async {
        final group = await showDialog<_Group>(
          context: context,
          builder: (_) => _GroupEditor(
            group: index == null ? null : draft[index],
            occupied: {
              for (var i = 0; i < draft.length; i++)
                if (i != index)
                  for (final app in (draft[i]['apps'] as List).cast<String>())
                    app: draft[i]['name'] as String,
            },
            nodes: nodes,
            chains: chains,
            android: android,
          ),
        );
        if (group == null || !context.mounted) return;
        refresh(() {
          if (index == null) {
            draft.add(group);
          } else {
            draft[index] = group;
          }
        });
      }

      return AlertDialog(
        title: const Text('软件分组分流'),
        scrollable: true,
        content: SizedBox(
          width: 460,
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            for (var i = 0; i < draft.length; i++)
              ListTile(
                contentPadding: EdgeInsets.zero,
                leading: Switch(
                  value: draft[i]['enabled'] as bool,
                  onChanged: (v) =>
                      refresh(() => draft[i] = {...draft[i], 'enabled': v}),
                ),
                title: Text(draft[i]['name'] as String),
                subtitle: Text(switch (draft[i]['targetType']) {
                  'node' => draft[i]['targetNode'] as String,
                  'chain' =>
                    '代理链 · ${chains?[draft[i]['targetChainId']] ?? "链已不可用"}',
                  'direct' => '直连 (DIRECT)',
                  'reject' => '拦截 (REJECT)',
                  final type => throw FormatException('未知路由目标 $type'),
                }),
                onTap: () => edit(i),
                trailing: IconButton(
                    tooltip: '删除分组',
                    icon: const Icon(Icons.delete_outline),
                    onPressed: () => refresh(() => draft.removeAt(i))),
              ),
            TextButton.icon(
                onPressed: () => edit(null),
                icon: const Icon(Icons.add),
                label: const Text('添加分组')),
          ]),
        ),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(context), child: const Text('取消')),
          FilledButton(
              onPressed: () => Navigator.pop(context, draft),
              child: const Text('保存')),
        ],
      );
    }),
  );
}

class _GroupEditor extends StatefulWidget {
  final _Group? group;
  final Map<String, String> occupied;
  final List<String> nodes;
  final Map<String, String>? chains;
  final bool android;
  const _GroupEditor(
      {required this.group,
      required this.occupied,
      required this.nodes,
      required this.chains,
      required this.android});

  @override
  State<_GroupEditor> createState() => _GroupEditorState();
}

class _GroupEditorState extends State<_GroupEditor> {
  final form = GlobalKey<FormState>();
  late String name;
  late String type;
  String? node;
  String? chainId;
  late final TextEditingController apps;
  String? pickerError;

  @override
  void initState() {
    super.initState();
    final group = widget.group;
    name = group == null ? '' : group['name'] as String;
    type = group == null ? 'node' : group['targetType'] as String;
    node = group?['targetNode'] as String?;
    chainId = group?['targetChainId'] as String?;
    apps = TextEditingController(
        text: group == null ? '' : (group['apps'] as List).join('\n'));
  }

  @override
  void dispose() {
    apps.dispose();
    super.dispose();
  }

  List<String> get entries => apps.text.trim().isEmpty
      ? []
      : apps.text
          .split(RegExp(r'\r?\n'))
          .map((s) => s.trim())
          .where((s) => s.isNotEmpty)
          .toList();
  bool invalid(String value) =>
      value.trim().isEmpty || value.contains(RegExp(r'[\x00-\x1f\x7f,*]'));

  String? conflict(String app) {
    String key(String s) => s.trim().toLowerCase().replaceAll('\\', '/');
    final a = key(app);
    for (final other in widget.occupied.entries) {
      final b = key(other.key);
      if (a == b ||
          (!widget.android &&
              ((!a.contains('/') && b.split('/').last == a) ||
                  (!b.contains('/') && a.split('/').last == b)))) {
        return '$app 已属于分组「${other.value}」';
      }
    }
    return null;
  }

  Future<void> pick() async {
    setState(() => pickerError = null);
    try {
      if (widget.android) {
        final rows = await const MethodChannel('smartvpn/host')
            .invokeListMethod<Map<Object?, Object?>>('listGames');
        if (!mounted) return;
        if (rows == null) throw const FormatException('未收到已安装应用列表');
        final selected = await showDialog<List<String>>(
          context: context,
          builder: (_) => InstalledGamePicker(
            apps: rows
                .map((row) => Map<String, String>.from(row))
                .where((row) => conflict(row['package']!) == null)
                .toList(),
            initial: entries
                .map((s) => s.trim())
                .where((s) => s.isNotEmpty)
                .toList(),
          ),
        );
        if (mounted && selected != null) apps.text = selected.join('\n');
      } else {
        final path = await const MethodChannel('smartvpn/lifecycle')
            .invokeMethod<String>('pickGame');
        if (mounted && path != null) {
          final message = conflict(path);
          if (message != null) {
            setState(() => pickerError = message);
          } else {
            apps.text = [...entries, path].join('\n');
          }
        }
      }
    } on PlatformException catch (e) {
      if (mounted) setState(() => pickerError = e.toString());
    } on FormatException catch (e) {
      if (mounted) setState(() => pickerError = e.message);
    }
  }

  void save() {
    if (!form.currentState!.validate()) return;
    final seen = <String>{};
    Navigator.pop(context, <String, dynamic>{
      'id': widget.group == null
          ? DateTime.now().microsecondsSinceEpoch.toString()
          : widget.group!['id'],
      'name': name.trim(),
      'targetType': type,
      if (type == 'node') 'targetNode': node,
      if (type == 'chain') 'targetChainId': chainId,
      'apps': entries
          .map((s) => s.trim())
          .where((s) => seen.add(s.toLowerCase()))
          .toList(),
      'enabled': widget.group == null || widget.group!['enabled'] == true,
    });
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
        title: Text(widget.group == null ? '添加分组' : '编辑分组'),
        scrollable: true,
        content: SizedBox(
            width: 460,
            child: Form(
                key: form,
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    TextFormField(
                        initialValue: name,
                        decoration: const InputDecoration(labelText: '分组名称'),
                        onChanged: (v) => name = v,
                        validator: (v) =>
                            invalid(v!) ? '请输入有效名称，不能包含逗号、换行或 *' : null),
                    DropdownButtonFormField<String>(
                        value: type,
                        decoration: const InputDecoration(labelText: '路由目标'),
                        items: [
                          const DropdownMenuItem(
                              value: 'node', child: Text('指定节点')),
                          if (widget.chains != null)
                            const DropdownMenuItem(
                                value: 'chain', child: Text('指定代理链')),
                          const DropdownMenuItem(
                              value: 'direct', child: Text('直连 (DIRECT)')),
                          const DropdownMenuItem(
                              value: 'reject', child: Text('拦截 (REJECT)')),
                        ],
                        onChanged: (v) => setState(() => type = v!)),
                    if (type == 'node')
                      DropdownButtonFormField<String>(
                          isExpanded: true,
                          value: widget.nodes.contains(node) ? node : null,
                          decoration: const InputDecoration(labelText: '目标节点'),
                          hint: Text(node != null
                              ? '节点已不可用：$node'
                              : widget.nodes.isEmpty
                                  ? '暂无可用节点'
                                  : '请选择节点'),
                          items: widget.nodes
                              .map((n) => DropdownMenuItem(
                                  value: n,
                                  child:
                                      Text(n, overflow: TextOverflow.ellipsis)))
                              .toList(),
                          onChanged: (v) => setState(() => node = v),
                          validator: (v) => v == null ? '请选择可用节点' : null),
                    if (widget.chains case final chains? when type == 'chain')
                      DropdownButtonFormField<String>(
                          isExpanded: true,
                          value: chains.containsKey(chainId) ? chainId : null,
                          decoration: const InputDecoration(labelText: '目标代理链'),
                          hint: Text(
                              chainId != null && !chains.containsKey(chainId)
                                  ? '原代理链已不可用，请重新选择'
                                  : chains.isEmpty
                                      ? '请先在线路页保存代理链'
                                      : '请选择代理链'),
                          items: [
                            for (final chain in chains.entries)
                              DropdownMenuItem(
                                  value: chain.key,
                                  child: Text(chain.value,
                                      overflow: TextOverflow.ellipsis))
                          ],
                          onChanged: chains.isEmpty
                              ? null
                              : (v) => setState(() => chainId = v),
                          validator: (v) => v == null ? '请选择可用代理链' : null),
                    TextFormField(
                        controller: apps,
                        minLines: 3,
                        maxLines: 6,
                        decoration: InputDecoration(
                            labelText: widget.android
                                ? '应用包名（每行一个）'
                                : '进程名或完整路径（每行一个）'),
                        validator: (_) {
                          if (entries.isEmpty) return '请至少填写或选择一个应用';
                          if (entries.any(invalid)) {
                            return '每行填写一个应用，不能包含逗号或 *';
                          }
                          for (final app in entries) {
                            final message = conflict(app);
                            if (message != null) return message;
                          }
                          return null;
                        }),
                    TextButton.icon(
                        onPressed: pick,
                        icon: const Icon(Icons.folder_open),
                        label: const Text('选择应用')),
                    if (pickerError != null)
                      Text(pickerError!,
                          style: TextStyle(
                              color: Theme.of(context).colorScheme.error)),
                  ],
                ))),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(context), child: const Text('取消')),
          FilledButton(onPressed: save, child: const Text('确定')),
        ],
      );
}
