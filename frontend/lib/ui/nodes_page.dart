part of '../main.dart';

/// Node browsing and selection: group by measured region or by the line kind the
/// provider put in the name, filter, and measure every node at once.
extension _NodesPage on _HomePageState {
  Widget _nodesPage() {
    final visible = _nodes
        .where((node) => nodeMatchesQuery(node, _nodeQuery))
        .toList(growable: false);
    final groups = groupNodes(
      nodes: visible,
      regions: _regionByName,
      grouping: _nodeGrouping,
    );

    final items = <_NodeListItem>[];
    for (final group in groups) {
      items.add(_NodeListItem.group(group.label, group.nodes.length));
      for (final node in group.nodes) {
        items.add(_NodeListItem.entry(node));
      }
    }

    return Column(children: [
      Padding(
        padding: const EdgeInsets.fromLTRB(28, 20, 28, 16),
        child: Center(
          child: ConstrainedBox(
            constraints:
                const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
            child: Column(children: [
              // The controls are the same on both layouts; what changes is
              // whether they sit beside the search field or below it. On a
              // phone the row is wider than the screen, and a button that
              // cannot be reached is worse than one on the next line.
              if (MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint) ...[
                _nodeSearchField(),
                const SizedBox(height: 8),
                Row(children: [
                  Expanded(
                    child: SegmentedButton<NodeGrouping>(
                      segments: const [
                        ButtonSegment(
                            value: NodeGrouping.region,
                            label: Text('按地区'),
                            icon: Icon(Icons.public_rounded, size: 16)),
                        ButtonSegment(
                            value: NodeGrouping.line,
                            label: Text('按线路'),
                            icon: Icon(Icons.route_rounded, size: 16)),
                        ButtonSegment(
                            value: NodeGrouping.source,
                            label: Text('按来源'),
                            icon: Icon(Icons.hub_outlined, size: 16)),
                      ],
                      selected: {_nodeGrouping},
                      onSelectionChanged: (selection) =>
                          _refresh(() => _nodeGrouping = selection.first),
                    ),
                  ),
                ]),
                const SizedBox(height: 8),
                Row(children: [
                  _measureAllNodesButton(),
                  const SizedBox(width: 8),
                  _reloadNodesButton(),
                ]),
              ] else
                Row(children: [
                  SegmentedButton<NodeGrouping>(
                    segments: const [
                      ButtonSegment(
                          value: NodeGrouping.region,
                          label: Text('按地区'),
                          icon: Icon(Icons.public_rounded, size: 16)),
                      ButtonSegment(
                          value: NodeGrouping.line,
                          label: Text('按线路'),
                          icon: Icon(Icons.route_rounded, size: 16)),
                      ButtonSegment(
                          value: NodeGrouping.source,
                          label: Text('按来源'),
                          icon: Icon(Icons.hub_outlined, size: 16)),
                    ],
                    selected: {_nodeGrouping},
                    onSelectionChanged: (selection) =>
                        _refresh(() => _nodeGrouping = selection.first),
                  ),
                  const SizedBox(width: 14),
                  Expanded(child: _nodeSearchField()),
                  const SizedBox(width: 10),
                  _measureAllNodesButton(),
                  const SizedBox(width: 8),
                  _reloadNodesButton(),
                ]),
              const SizedBox(height: 14),
              if (MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint) ...[
                Wrap(
                  spacing: 8,
                  runSpacing: 6,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    const Text('选择模式', style: Tokens.hint),
                    _ModeChip(
                      label: '自动选择',
                      selected:
                          _lockedRegion.isEmpty && _selectedNode == autoGroup,
                      onTap: () => unawaited(_selectNode(autoGroup)),
                    ),
                    _ModeChip(
                      label: '故障转移',
                      selected:
                          _lockedRegion.isEmpty && _selectedNode == fallbackGroup,
                      onTap: () => unawaited(_selectNode(fallbackGroup)),
                    ),
                  ],
                ),
                const SizedBox(height: 8),
                Align(alignment: Alignment.centerLeft, child: _lockHint()),
                const SizedBox(height: 10),
                Align(alignment: Alignment.centerLeft, child: _addNodeButton()),
              ] else
                Row(children: [
                  const Text('选择模式', style: Tokens.hint),
                  const SizedBox(width: 12),
                  _ModeChip(
                    label: '自动选择',
                    selected:
                        _lockedRegion.isEmpty && _selectedNode == autoGroup,
                    onTap: () => unawaited(_selectNode(autoGroup)),
                  ),
                  const SizedBox(width: 8),
                  _ModeChip(
                    label: '故障转移',
                    selected:
                        _lockedRegion.isEmpty && _selectedNode == fallbackGroup,
                    onTap: () => unawaited(_selectNode(fallbackGroup)),
                  ),
                  const SizedBox(width: 14),
                  Expanded(child: _lockHint()),
                  const SizedBox(width: 10),
                  _addNodeButton(),
                ]),
            ]),
          ),
        ),
      ),
      const Divider(height: 1),
      Expanded(
        child: groups.isEmpty
            ? Center(
                child: ConstrainedBox(
                  constraints:
                      const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
                  child: _nodes.isEmpty
                      ? _EmptyState(
                          text: '还没有可选节点。先到「订阅与内核」获取订阅节点。',
                          actionLabel: '去获取节点',
                          onAction: () => _goTo(_AppPage.subscription),
                        )
                      : _EmptyState(
                          text: '没有名称匹配「$_nodeQuery」的节点。',
                          actionLabel: '清除搜索',
                          onAction: () => _refresh(() {
                            _nodeSearch.clear();
                            _nodeQuery = '';
                          }),
                        ),
                ),
              )
            : ListView.builder(
                padding: const EdgeInsets.symmetric(vertical: 8),
                itemCount: items.length,
                itemBuilder: (context, index) {
                  final item = items[index];
                  return Center(
                    child: ConstrainedBox(
                      constraints: const BoxConstraints(
                          maxWidth: Tokens.contentMaxWidth),
                      child: switch (item) {
                        _GroupRow(:final label, :final count) =>
                          _groupRow(label, count),
                        _EntryRow(:final node) => _nodeRow(node),
                      },
                    ),
                  );
                },
              ),
      ),
    ]);
  }

  Widget _groupRow(String label, int count) => Padding(
        padding: const EdgeInsets.fromLTRB(14, 18, 14, 6),
        child: Row(children: [
          Text(label, style: Tokens.section),
          const SizedBox(width: 8),
          Text('$count', style: Tokens.number.copyWith(color: Tokens.inkFaint)),
        ]),
      );

  Widget _nodeRow(ProxyNode node) {
    final health = _nodeHealth[node.name];
    final region = _regionByName[node.name];
    final selected = node.name == _selectedNode;
    final active = node.name == _activeNode;
    final latency = _nodeLatency[node.name] ?? health?.latencyMs ?? 0;
    final country =
        (region != null && region.country.isNotEmpty) ? region.country : '—';

    final latencyLabel = Text(latency > 0 ? '$latency ms' : '—',
        textAlign: TextAlign.right, style: Tokens.number);
    final healthBadge = _Badge(
      label: nodeHealthLabel(health?.state),
      color: nodeHealthDot(health?.state),
      tint: Tokens.idleTint,
    );
    final markBadge = active
        ? const _Badge(
            label: '使用中', color: Tokens.jade, tint: Tokens.jadeTint)
        : (selected
            ? const _Badge(label: '已选', color: Tokens.jade, tint: Tokens.jadeTint)
            : const SizedBox.shrink());
    final meta = Row(children: [
      SizedBox(width: 34, child: Text(country, style: Tokens.hint)),
      SizedBox(width: 78, child: Text(node.type, style: Tokens.hint)),
      SizedBox(
          width: 64,
          child: Text(node.network.isEmpty ? '—' : node.network,
              style: Tokens.hint)),
      if (node.tls) const Text('TLS', style: Tokens.hint),
      // A pasted node is worth marking: it is the one the subscription does not
      // bring back after a refresh.
      if (node.manual) ...[
        const SizedBox(width: 8),
        const Text('手动', style: TextStyle(fontSize: 12, color: Tokens.jade)),
      ],
    ]);
    final deleteButton = node.manual
        ? _AkHoverIconButton(
            padding: EdgeInsets.zero,
            size: 16,
            tooltip: '删除这个手动节点',
            previewCode: 'DELETE',
            color: Tokens.bad,
            onPressed: _busy || _kernelRunning
                ? null
                : () => unawaited(_deleteManualNode(node.name)),
            icon: const Icon(Icons.delete_outline_rounded, size: 16),
          )
        : null;

    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    final watermark = country != '—' && country.isNotEmpty
        ? country.toUpperCase()
        : (node.type.isNotEmpty ? node.type.toUpperCase() : 'NODE');
    final previewTag = latency > 0 ? '$latency MS' : 'PRTS // ${node.type.toUpperCase()}';

    return _AkHoverRow(
      onTap: _busy ? null : () => unawaited(_selectNode(node.name)),
      selected: selected || active,
      accentColor: active ? Tokens.jade : (selected ? Tokens.cyan : Tokens.cyanNeon),
      watermark: watermark,
      previewTag: previewTag,
      padding: const EdgeInsets.fromLTRB(11, 8, 14, 8),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        _StatusDot(color: nodeHealthDot(health?.state)),
        const SizedBox(width: 10),
        Expanded(
          child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(node.name,
                    overflow: TextOverflow.ellipsis,
                    style: const TextStyle(
                        fontSize: 13, fontWeight: FontWeight.w500)),
                const SizedBox(height: 2),
                meta,
                if (narrow) ...[
                  const SizedBox(height: 4),
                  Row(children: [
                    latencyLabel,
                    const SizedBox(width: 10),
                    healthBadge,
                    if (node.manual || active || selected) ...[
                      const SizedBox(width: 8),
                      markBadge,
                    ],
                    const Spacer(),
                    if (deleteButton != null)
                      SizedBox(width: 34, child: deleteButton),
                  ]),
                ],
              ]),
        ),
        if (!narrow) ...[
          const SizedBox(width: 12),
          if (deleteButton != null)
            SizedBox(width: 34, child: deleteButton),
          SizedBox(width: 74, child: latencyLabel),
          const SizedBox(width: 14),
          SizedBox(
            width: 64,
            child: Align(alignment: Alignment.centerRight, child: healthBadge),
          ),
          SizedBox(
            width: 58,
            child:
                Align(alignment: Alignment.centerRight, child: markBadge),
          ),
        ],
      ]),
    );
  }

  /// The controls of the two filter rows, built once so that the wide and the
  /// narrow layout show the same ones rather than two copies that can drift.
  Widget _nodeSearchField() => TextField(
        controller: _nodeSearch,
        onChanged: (value) => _refresh(() => _nodeQuery = value),
        decoration: const InputDecoration(
          hintText: '搜索节点名称',
          prefixIcon: Icon(Icons.search_rounded, size: 18),
        ),
      );

  Widget _measureAllNodesButton() => _AkHoverButtonWrap(
        color: Tokens.cyan,
        child: OutlinedButton.icon(
          onPressed: _busy || !_connected ? null : _measureAllNodes,
          icon: const Icon(Icons.speed_rounded, size: 16),
          label: Text(_measuringNodes ? '测速中…' : '测试延迟'),
        ),
      );

  Widget _reloadNodesButton() => _AkHoverButtonWrap(
        color: Tokens.cyan,
        child: TextButton.icon(
          onPressed:
              _busy ? null : (_connected ? _refreshNodes : _fetchNodes),
          icon: const Icon(Icons.refresh_rounded, size: 16),
          label: Text(_connected ? '刷新' : '重新获取'),
        ),
      );

  /// What selecting a node does while a region is locked, which is nothing: the
  /// lock owns the choice, and saying so is better than a control that appears
  /// to work.
  Widget _lockHint() => Text(
        _lockedRegion.isEmpty
            ? '点击下方节点即可选用，并把地区锁定到它的实测地区。'
            : '已锁定地区 $_lockedRegion，节点只会在该地区的候选内自动维护，以上选择不再生效。',
        style: Tokens.hint,
      );

  Widget _addNodeButton() => _AkHoverButtonWrap(
        color: Tokens.cyan,
        child: OutlinedButton.icon(
          onPressed: _busy ? null : () => unawaited(_showAddNodeDialog()),
          icon: const Icon(Icons.add_rounded, size: 16),
          label: const Text('添加节点'),
        ),
      );
}

sealed class _NodeListItem {
  const _NodeListItem();

  const factory _NodeListItem.group(String label, int count) = _GroupRow;
  const factory _NodeListItem.entry(ProxyNode node) = _EntryRow;
}

class _GroupRow extends _NodeListItem {
  const _GroupRow(this.label, this.count);
  final String label;
  final int count;
}

class _EntryRow extends _NodeListItem {
  const _EntryRow(this.node);
  final ProxyNode node;
}

class _ModeChip extends StatelessWidget {
  const _ModeChip(
      {required this.label, required this.selected, required this.onTap});

  final String label;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(Tokens.radiusBadge),
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 6),
          decoration: BoxDecoration(
            color: selected ? Tokens.jadeTint : Tokens.surface,
            border: Border.all(
                color: selected ? Tokens.jade : Tokens.hairline),
            borderRadius: BorderRadius.circular(Tokens.radiusBadge),
          ),
          child: Text(label,
              style: TextStyle(
                fontSize: 12.5,
                fontWeight: FontWeight.w500,
                color: selected ? Tokens.jade : Tokens.inkMuted,
              )),
        ),
      );
}
