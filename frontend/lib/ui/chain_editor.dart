import 'package:flutter/material.dart';
import 'probe_book.dart';

class ChainEditor extends StatefulWidget {
  final List<String> available;
  final Map<String, String> nodeGroups;
  final Map<String, String> nodeTransports;
  final List<String> initialHops;
  final String initialName;
  final Map<String, List<String>>? initialBackups;
  final Map<String,String> sessionTypes, initialSessions;
  final bool tunnelActive;
  final ProbeBook book;
  final SpeedBook speeds;
  final Future<void> Function(
      String name, List<String> hops, Map<String, List<String>> backups, Map<String,String> sessions) save;

  const ChainEditor({
    super.key,
    required this.available,
    this.nodeGroups = const {},
    this.nodeTransports = const {},
    required this.initialHops,
    required this.initialName,
    this.initialBackups,
    this.sessionTypes = const {},
    this.initialSessions = const {},
    this.tunnelActive = false,
    required this.book,
    required this.speeds,
    required this.save,
  });

  @override
  State<ChainEditor> createState() => _ChainEditorState();
}

class _ChainEditorState extends State<ChainEditor> {
  late final TextEditingController _nameController;
  late List<String> _hops;

  late final Map<String, List<String>> _backups;
  Set<String> get _used => {..._hops, ..._backups.values.expand((v) => v)};
  ProbeBook get _pathBook => widget.book.forBackups(_backups);
  late final Map<String,String> _sessions;
  bool _isSaving = false;
  String? _globalError;

  @override
  void initState() {
    super.initState();
    _nameController = TextEditingController(text: widget.initialName);
    _sessions = Map.of(widget.initialSessions);
    _backups = {
      for (final e
          in (widget.initialBackups ?? <String, List<String>>{}).entries)
        e.key: List.of(e.value)
    };
    _hops = List<String>.from(widget.initialHops);
  }

  @override
  void dispose() {
    _nameController.dispose();
    super.dispose();
  }

  void _onChainModified() {
    setState(() {
      _globalError = null;
    });
  }

  void _swap(int indexA, int indexB) {
    if (indexA < 0 ||
        indexA >= _hops.length ||
        indexB < 0 ||
        indexB >= _hops.length) {
      return;
    }
    setState(() {
      final temp = _hops[indexA];
      _hops[indexA] = _hops[indexB];
      _hops[indexB] = temp;
      _onChainModified();
    });
  }

  void _remove(int index) {
    if (index < 0 || index >= _hops.length) return;
    setState(() {
      _backups.remove(_hops.removeAt(index));
      _onChainModified();
    });
  }

  Future<void> _replace(int index) async {
    final current = _hops[index];
    final candidates = widget.available
        .where((n) => !_used.contains(n) || n == current)
        .toList();

    final selected = await showModalBottomSheet<String>(
      context: context,
      isScrollControlled: true,
      builder: (ctx) => _NodeSelectionSheet(
        book: _pathBook,
        prefix: _hops.take(index).toList(),
        title: '替换第 ${index + 1} 跳节点',
        current: current,
        nodes: candidates,
        nodeGroups: widget.nodeGroups,
        nodeTransports: widget.nodeTransports,
      ),
    );

    if (selected != null && selected != current && mounted) {
      setState(() {
        final backups = _backups.remove(current);
        if (backups != null) _backups[selected] = backups;
        _hops[index] = selected;
        _onChainModified();
      });
    }
  }

  Future<void> _addNode() async {
    if (_hops.length >= 8) {
      setState(() => _globalError = '节点链最多支持 8 个节点');
      return;
    }

    final candidates =
        widget.available.where((n) => !_used.contains(n)).toList();
    if (candidates.isEmpty) {
      setState(() => _globalError = '没有更多可选的非重复节点');
      return;
    }

    final selected = await showModalBottomSheet<String>(
      context: context,
      isScrollControlled: true,
      builder: (ctx) => _NodeSelectionSheet(
        book: _pathBook,
        prefix: List.of(_hops),
        title: '选择第 ${_hops.length + 1} 跳节点',
        nodes: candidates,
        nodeGroups: widget.nodeGroups,
        nodeTransports: widget.nodeTransports,
      ),
    );

    if (selected != null && mounted) {
      setState(() {
        _hops.add(selected);
        _onChainModified();
      });
    }
  }

  Future<void> _addBackup(int index) async {
    final primary = _hops[index];
    final selected = await showModalBottomSheet<String>(
      context: context,
      isScrollControlled: true,
      builder: (_) => _NodeSelectionSheet(
          book: _pathBook,
          prefix: _hops.take(index).toList(),
          title: '选择第 ${index + 1} 跳的备用节点',
          nodes: widget.available.where((n) => !_used.contains(n)).toList(),
          nodeGroups: widget.nodeGroups,
          nodeTransports: widget.nodeTransports),
    );
    if (mounted && selected != null) {
      setState(() => (_backups[primary] ??= []).add(selected));
    }
  }

  Future<void> _handleSave() async {
    final name = _nameController.text.trim();
    if (name.isEmpty) {
      setState(() => _globalError = '链名称不能为空');
      return;
    }
    if (_hops.length < 2 || _hops.length > 8) {
      setState(() => _globalError = '节点数量必须在 2 至 8 个之间');
      return;
    }
    if (_hops.toSet().length != _hops.length) {
      setState(() => _globalError = '节点链中不能包含重复节点');
      return;
    }

    setState(() {
      _isSaving = true;
      _globalError = null;
    });

    try {
      await widget.save(name, List<String>.unmodifiable(_hops), _backups, {for (final n in _used) if (_sessions.containsKey(n)) n: _sessions[n]!});
      if (!mounted) return;
      Navigator.of(context).pop(true);
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _isSaving = false;
        _globalError = '保存失败: ${e.toString().replaceAll('Exception: ', '')}';
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final canAdd = _hops.length < 8 &&
        widget.available.any((item) => !_used.contains(item));

    return Scaffold(
      appBar: AppBar(
        title: const Text('编辑代理链', style: TextStyle(fontSize: 18)),
        centerTitle: false,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          tooltip: '取消并返回',
          onPressed: _isSaving ? null : () => Navigator.of(context).pop(false),
        ),
        actions: [
          Padding(
            padding:
                const EdgeInsets.symmetric(horizontal: 12.0, vertical: 8.0),
            child: FilledButton.icon(
              onPressed: _isSaving ? null : _handleSave,
              icon: _isSaving
                  ? const SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(
                        strokeWidth: 2,
                        color: Colors.white,
                      ),
                    )
                  : const Icon(Icons.save, size: 18),
              label: Text(_isSaving ? '保存中' : '保存'),
            ),
          ),
        ],
      ),
      body: SafeArea(
        child: Column(
          children: [
            Expanded(
              child: ListView(
                padding: const EdgeInsets.all(12.0),
                children: [
                  if (widget.tunnelActive)
                    const Padding(
                        padding: EdgeInsets.only(bottom: 12),
                        child:
                            Text('当前 TUN 可能接管到第一跳的连接。要测本机直达入口的延迟，请先断开 TUN。')),
                  TextField(
                    controller: _nameController,
                    decoration: InputDecoration(
                      labelText: '代理链名称',
                      hintText: '请输入自定义代理链名称',
                      isDense: true,
                      border: const OutlineInputBorder(),
                      prefixIcon: const Icon(Icons.label_outline, size: 20),
                      suffixIcon: IconButton(
                        icon: const Icon(Icons.clear, size: 18),
                        onPressed: () => _nameController.clear(),
                      ),
                    ),
                  ),
                  const SizedBox(height: 12),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      Text(
                        '转发顺序 (${_hops.length}/8)',
                        style: theme.textTheme.titleSmall?.copyWith(
                          fontWeight: FontWeight.bold,
                        ),
                      ),
                      Text(
                        '2 至 8 跳',
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.outline,
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 8),
                  if (_globalError != null)
                    Container(
                      margin: const EdgeInsets.only(bottom: 8.0),
                      padding: const EdgeInsets.symmetric(
                          horizontal: 10.0, vertical: 8.0),
                      decoration: BoxDecoration(
                        color: theme.colorScheme.errorContainer,
                        borderRadius: BorderRadius.circular(8),
                      ),
                      child: Row(
                        children: [
                          Icon(Icons.error_outline,
                              size: 18,
                              color: theme.colorScheme.onErrorContainer),
                          const SizedBox(width: 8),
                          Expanded(
                            child: Text(
                              _globalError!,
                              style: TextStyle(
                                fontSize: 13,
                                color: theme.colorScheme.onErrorContainer,
                              ),
                            ),
                          ),
                        ],
                      ),
                    ),
                  for (int i = 0; i < _hops.length; i++)
                    _buildHopCard(context, i, theme),
                  const SizedBox(height: 8),
                  OutlinedButton.icon(
                    style: OutlinedButton.styleFrom(
                      padding: const EdgeInsets.symmetric(vertical: 12),
                      shape: RoundedRectangleBorder(
                        borderRadius: BorderRadius.circular(8),
                      ),
                    ),
                    onPressed: canAdd ? _addNode : null,
                    icon: const Icon(Icons.add_circle_outline, size: 18),
                    label: Text(
                      _hops.length >= 8
                          ? '已达到最大 8 跳限制'
                          : (canAdd ? '追加下一跳节点' : '没有更多可用节点'),
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildHopCard(BuildContext context, int index, ThemeData theme) {
    final hopName = _hops[index];
    return Card(
      margin: const EdgeInsets.only(bottom: 8.0),
      elevation: 0.5,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(8),
        side: BorderSide(color: theme.colorScheme.outlineVariant),
      ),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 10.0, vertical: 8.0),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                CircleAvatar(
                  radius: 12,
                  backgroundColor: theme.colorScheme.primaryContainer,
                  child: Text(
                    '${index + 1}',
                    style: TextStyle(
                      fontSize: 12,
                      fontWeight: FontWeight.bold,
                      color: theme.colorScheme.onPrimaryContainer,
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    hopName,
                    softWrap: true,
                    style: const TextStyle(
                      fontSize: 14,
                      fontWeight: FontWeight.w600,
                      height: 1.3,
                    ),
                  ),
                ),
                const SizedBox(width: 8),
              ],
            ),
            ..._sessionOption(hopName),
            Wrap(children: [
              PathProbeButton(
                  book: widget.book, hops: [hopName], title: '测单节点'),
              if (index > 0)
                PathProbeButton(
                    book: _pathBook,
                    hops: _hops.sublist(0, index + 1),
                    title: index == _hops.length - 1 ? '测整链' : '测前置路径'),
              if (index == _hops.length - 1)
                PathSpeedButton(book: widget.speeds, hops: _hops, backups: _backups),
            ]),
            if (widget.initialBackups != null) ...[
              for (final backup in _backups[hopName] ?? <String>[])
                ListTile(
                    dense: true,
                    title: Text(backup),
                    leading: const Icon(Icons.alt_route),
                    trailing: IconButton(
                        tooltip: '移除备用节点',
                        icon: const Icon(Icons.close),
                        onPressed: () => setState(() {
                              final members = _backups[hopName]!;
                              members.remove(backup);
                              if (members.isEmpty) _backups.remove(hopName);
                            }))),
              for (final backup in _backups[hopName] ?? <String>[]) ..._sessionOption(backup),
              TextButton.icon(
                  icon: const Icon(Icons.add),
                  label: const Text('添加备用节点'),
                  onPressed: widget.available.any((n) => !_used.contains(n))
                      ? () => _addBackup(index)
                      : null),
            ],
            const Divider(height: 12, thickness: 0.5),
            Row(
              mainAxisAlignment: MainAxisAlignment.end,
              children: [
                _buildActionBtn(
                  icon: Icons.arrow_upward,
                  tooltip: '上移',
                  onPressed: index > 0 ? () => _swap(index, index - 1) : null,
                ),
                _buildActionBtn(
                  icon: Icons.arrow_downward,
                  tooltip: '下移',
                  onPressed: index < _hops.length - 1
                      ? () => _swap(index, index + 1)
                      : null,
                ),
                _buildActionBtn(
                  icon: Icons.swap_horiz,
                  tooltip: '替换',
                  onPressed: () => _replace(index),
                ),
                _buildActionBtn(
                  icon: Icons.delete_outline,
                  tooltip: '移除',
                  color: theme.colorScheme.error,
                  onPressed: () => _remove(index),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  List<Widget> _sessionOption(String node) {
    final type = widget.sessionTypes[node];
    final mux = const {'vmess','vless','ss','trojan'}.contains(type);
    final quic = const {'hysteria2','hy2','tuic'}.contains(type);
    if (!mux && !quic) return [];
    final mode = mux ? 'mux' : 'keepalive';
    return [SwitchListTile(
      title: Text(mux ? '$node · TCP 多路复用' : '$node · QUIC 长连接保活'),
      subtitle: Text(mux ? '服务端需启用 sing-mux（Yamux）' : '启用后心跳间隔最长 5 秒'),
      value: _sessions[node] == mode,
      onChanged: (on) => setState(() { if (on) { _sessions[node] = mode; } else { _sessions.remove(node); } }),
    )];
  }

  Widget _buildActionBtn({
    required IconData icon,
    required String tooltip,
    required VoidCallback? onPressed,
    Color? color,
  }) {
    return IconButton(
      visualDensity: VisualDensity.compact,
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
      constraints: const BoxConstraints(),
      icon: Icon(icon, size: 18, color: color),
      tooltip: tooltip,
      onPressed: onPressed,
    );
  }
}

class _NodeSelectionSheet extends StatefulWidget {
  final String title;
  final String? current;
  final List<String> nodes;
  final Map<String, String> nodeGroups;
  final Map<String, String> nodeTransports;
  final ProbeBook book;
  final List<String> prefix;

  const _NodeSelectionSheet({
    required this.title,
    this.current,
    required this.nodes,
    required this.nodeGroups,
    this.nodeTransports = const {},
    required this.book,
    required this.prefix,
  });

  @override
  State<_NodeSelectionSheet> createState() => _NodeSelectionSheetState();
}

class _NodeSelectionSheetState extends State<_NodeSelectionSheet> {
  final _search = TextEditingController();
  final _collapsed = <String>{};
  String? _selectedGroup;

  String _groupOf(String node) {
    final mapped = widget.nodeGroups[node]?.trim();
    if (mapped != null && mapped.isNotEmpty) return mapped;
    final name = node.trim();
    final end = name.indexOf(']');
    return name.startsWith('[') && end > 1 ? name.substring(0, end + 1) : '默认源';
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  Widget _chip(String? group, int count) => Padding(
        padding: const EdgeInsets.only(right: 6),
        child: ChoiceChip(
          label: Text('${group ?? '全部'} ($count)'),
          selected: _selectedGroup == group,
          onSelected: (_) => setState(() => _selectedGroup = group),
        ),
      );

  Widget _nodeTile(String node) {
    final current = node == widget.current;
    final transport = widget.nodeTransports[node] ?? '';
    return ListTile(
      dense: true,
      title: Text(transport.isEmpty ? node : '$node [$transport]',
          style: TextStyle(
            fontWeight: current ? FontWeight.bold : FontWeight.normal,
            color: current ? Theme.of(context).colorScheme.primary : null,
          )),
      subtitle: Wrap(children: [
        PathProbeButton(book: widget.book, hops: [node], title: '测单节点'),
        if (widget.prefix.isNotEmpty)
          PathProbeButton(
              book: widget.book, hops: [...widget.prefix, node], title: '测此路径'),
      ]),
      onTap: () => Navigator.of(context).pop(node),
    );
  }

  @override
  Widget build(BuildContext context) {
    final query = _search.text.trim().toLowerCase();
    final matches = <String, List<String>>{};
    var total = 0;
    for (final node in widget.nodes) {
      final group = _groupOf(node);
      final bucket = matches.putIfAbsent(group, () => []);
      if (group.toLowerCase().contains(query) ||
          node.toLowerCase().contains(query)) {
        bucket.add(node);
        total++;
      }
    }
    final groups = [
      ...matches.keys.where((g) => g != '默认源' && g != '手动节点'),
      if (matches.containsKey('默认源')) '默认源',
      if (matches.containsKey('手动节点')) '手动节点',
    ];
    final visible = groups.where((g) =>
        (_selectedGroup == null || g == _selectedGroup) &&
        matches[g]!.isNotEmpty);
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: DraggableScrollableSheet(
        initialChildSize: 0.75,
        minChildSize: 0.35,
        maxChildSize: 0.95,
        expand: false,
        builder: (context, controller) => CustomScrollView(
          controller: controller,
          slivers: [
            SliverToBoxAdapter(
                child: Padding(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
              child: Column(children: [
                Row(children: [
                  Expanded(
                      child: Text(widget.title,
                          style: Theme.of(context).textTheme.titleMedium)),
                  TextButton(
                      onPressed: () => Navigator.of(context).pop(),
                      child: const Text('取消')),
                ]),
                TextField(
                  controller: _search,
                  onChanged: (_) => setState(() {}),
                  decoration: InputDecoration(
                    isDense: true,
                    hintText: '搜索节点名或订阅分组',
                    prefixIcon: const Icon(Icons.search),
                    suffixIcon: _search.text.isEmpty
                        ? null
                        : IconButton(
                            tooltip: '清除搜索',
                            icon: const Icon(Icons.clear),
                            onPressed: () => setState(_search.clear),
                          ),
                  ),
                ),
                if (groups.length > 1)
                  SingleChildScrollView(
                    scrollDirection: Axis.horizontal,
                    child: Row(children: [
                      _chip(null, total),
                      for (final group in groups)
                        _chip(group, matches[group]!.length),
                    ]),
                  ),
              ]),
            )),
            if (visible.isEmpty)
              SliverFillRemaining(
                  hasScrollBody: false,
                  child: Center(
                    child: Column(mainAxisSize: MainAxisSize.min, children: [
                      const Text('未找到匹配的节点'),
                      TextButton(
                          onPressed: () => setState(() {
                                _search.clear();
                                _selectedGroup = null;
                              }),
                          child: const Text('清除筛选')),
                    ]),
                  )),
            for (final group in visible) ...[
              SliverToBoxAdapter(
                  child: ListTile(
                dense: true,
                title: Text(group),
                trailing: Row(mainAxisSize: MainAxisSize.min, children: [
                  Text('${matches[group]!.length}'),
                  Icon(query.isEmpty && _collapsed.contains(group)
                      ? Icons.expand_more
                      : Icons.expand_less),
                ]),
                enabled: query.isEmpty,
                onTap: () => setState(() {
                  if (!_collapsed.remove(group)) _collapsed.add(group);
                }),
              )),
              if (query.isNotEmpty || !_collapsed.contains(group))
                SliverList.builder(
                    itemCount: matches[group]!.length,
                    itemBuilder: (_, index) =>
                        _nodeTile(matches[group]![index])),
            ],
          ],
        ),
      ),
    );
  }
}
