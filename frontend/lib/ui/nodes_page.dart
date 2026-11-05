part of '../main.dart';

/// The Rhodes Island Route Matrix (线路 / 节点).
/// Search, group by region/line/source, measure all node latencies, and select routes.
extension _NodesPage on _HomePageState {
  Widget _nodesPage() {
    final narrow = MediaQuery.sizeOf(context).width < 1100;
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
        padding: EdgeInsets.fromLTRB(
          narrow ? 14 : 28,
          narrow ? 12 : 20,
          narrow ? 14 : 28,
          14,
        ),
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                // Top Search and Grouping Controls
                if (narrow) ...[
                  _nodeSearchField(),
                  const SizedBox(height: 10),
                  Row(children: [
                    Expanded(child: _groupingSegmentedButton()),
                  ]),
                  const SizedBox(height: 10),
                  Wrap(
                    spacing: 8,
                    runSpacing: 8,
                    children: [
                      _measureAllNodesButton(),
                      _reloadNodesButton(),
                      _addNodeButton(),
                    ],
                  ),
                ] else
                  Row(children: [
                    _groupingSegmentedButton(),
                    const SizedBox(width: 14),
                    Expanded(child: _nodeSearchField()),
                    const SizedBox(width: 10),
                    _measureAllNodesButton(),
                    const SizedBox(width: 8),
                    _reloadNodesButton(),
                    const SizedBox(width: 8),
                    _addNodeButton(),
                  ]),
                const SizedBox(height: 14),

                // Mode Selection and Lock Hint
                Wrap(
                  spacing: 10,
                  runSpacing: 8,
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
                      selected: _lockedRegion.isEmpty &&
                          _selectedNode == fallbackGroup,
                      onTap: () => unawaited(_selectNode(fallbackGroup)),
                    ),
                    const SizedBox(width: 4),
                    _lockHint(),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
      const Divider(height: 1),
      Expanded(
        child: groups.isEmpty
            ? SingleChildScrollView(
                child: ConstrainedBox(
                  constraints:
                      const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
                  child: _nodes.isEmpty
                      ? _EmptyState(
                          text: '尚未检索到可选节点。先前往「订阅」页面获取或合并节点。',
                          actionLabel: '前往获取节点',
                          onAction: () => _goTo(_AppPage.subscription),
                          secondaryActionLabel: '手动添加节点',
                          onSecondaryAction: () =>
                              unawaited(_showAddNodeDialog()),
                        )
                      : _EmptyState(
                          text: '没有名称匹配「$_nodeQuery」的节点。',
                          actionLabel: '清除搜索条件',
                          onAction: () => _refresh(() {
                            _nodeSearch.clear();
                            _nodeQuery = '';
                          }),
                        ),
                ),
              )
            : ListView.builder(
                padding: const EdgeInsets.symmetric(vertical: 6),
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

  Widget _groupingSegmentedButton() => SegmentedButton<NodeGrouping>(
        segments: const [
          ButtonSegment(
            value: NodeGrouping.region,
            label: Text('按地区'),
            icon: Icon(Icons.public_rounded, size: 16),
          ),
          ButtonSegment(
            value: NodeGrouping.line,
            label: Text('按线路'),
            icon: Icon(Icons.route_rounded, size: 16),
          ),
          ButtonSegment(
            value: NodeGrouping.source,
            label: Text('按来源'),
            icon: Icon(Icons.hub_outlined, size: 16),
          ),
        ],
        selected: {_nodeGrouping},
        onSelectionChanged: (selection) =>
            _refresh(() => _nodeGrouping = selection.first),
      );

  Widget _groupRow(String label, int count) => Padding(
        padding: const EdgeInsets.fromLTRB(16, 16, 16, 6),
        child: Row(children: [
          Container(
            width: 3,
            height: 12,
            color: Tokens.yellow,
          ),
          const SizedBox(width: 6),
          Text(label, style: Tokens.section),
          const SizedBox(width: 8),
          _Badge(
            label: '$count',
            color: Tokens.inkMuted,
            tint: Tokens.idleTint,
          ),
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
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;

    final latencyLabel = Text(
      latency > 0 ? '$latency ms' : '—',
      textAlign: TextAlign.right,
      style: Tokens.number.copyWith(
        color: latency > 0
            ? (latency < 160
                ? Tokens.ok
                : (latency < 320 ? Tokens.yellow : Tokens.bad))
            : Tokens.inkFaint,
        fontWeight: FontWeight.w700,
      ),
    );

    final healthBadge = _Badge(
      label: nodeHealthLabel(health?.state),
      color: nodeHealthDot(health?.state),
      tint: Tokens.idleTint,
    );

    final markBadge = active
        ? const _Badge(
            label: '使用中',
            color: Tokens.ok,
            tint: Tokens.okTint,
            showDot: true,
          )
        : (selected
            ? const _Badge(
                label: '已选',
                color: Tokens.yellow,
                tint: Tokens.yellowTint,
                showDot: true,
              )
            : const SizedBox.shrink());

    final meta = Wrap(
      spacing: 10,
      runSpacing: 4,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        if (country != '—')
          Text(country, style: Tokens.hint.copyWith(color: Tokens.inkMuted)),
        Text(node.type, style: Tokens.hint),
        if (node.network.isNotEmpty && node.network != '—')
          Text(node.network, style: Tokens.hint),
        if (node.tls)
          const Text('TLS', style: TextStyle(fontSize: 11, color: Tokens.cyan)),
        if (node.manual)
          const _Badge(
            label: '手动节点',
            color: Tokens.yellow,
            tint: Tokens.yellowTint,
          ),
      ],
    );

    final deleteButton = node.manual
        ? _AkHoverIconButton(
            padding: const EdgeInsets.all(8),
            size: 16,
            tooltip: '删除此手动节点',
            color: Tokens.bad,
            onPressed: _busy || _kernelRunning
                ? null
                : () => unawaited(_deleteManualNode(node.name)),
            icon: const Icon(Icons.delete_outline_rounded, size: 16),
          )
        : null;

    return _AkHoverRow(
      onTap: _busy ? null : () => unawaited(_selectNode(node.name)),
      selected: selected || active,
      accentColor:
          active ? Tokens.ok : (selected ? Tokens.yellow : Tokens.cyan),
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.center,
        children: [
          _StatusDot(color: nodeHealthDot(health?.state), size: 8),
          const SizedBox(width: 10),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(
                  node.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontSize: 13,
                    fontWeight:
                        active || selected ? FontWeight.w700 : FontWeight.w500,
                    color: active ? Tokens.ok : Tokens.ink,
                  ),
                ),
                const SizedBox(height: 3),
                meta,
                if (narrow) ...[
                  const SizedBox(height: 6),
                  Row(children: [
                    latencyLabel,
                    const SizedBox(width: 8),
                    healthBadge,
                    if (active || selected) ...[
                      const SizedBox(width: 8),
                      markBadge,
                    ],
                    const Spacer(),
                    if (deleteButton != null) deleteButton,
                  ]),
                ],
              ],
            ),
          ),
          if (!narrow) ...[
            const SizedBox(width: 12),
            if (deleteButton != null) deleteButton,
            SizedBox(width: 76, child: latencyLabel),
            const SizedBox(width: 10),
            SizedBox(
              width: 68,
              child:
                  Align(alignment: Alignment.centerRight, child: healthBadge),
            ),
            const SizedBox(width: 6),
            SizedBox(
              width: 64,
              child: Align(alignment: Alignment.centerRight, child: markBadge),
            ),
          ],
        ],
      ),
    );
  }

  Widget _nodeSearchField() => TextField(
        controller: _nodeSearch,
        onChanged: (value) => _refresh(() => _nodeQuery = value),
        decoration: InputDecoration(
          hintText: '搜索节点名称',
          prefixIcon: const Icon(Icons.search_rounded, size: 18),
          suffixIcon: _nodeQuery.isNotEmpty
              ? IconButton(
                  icon: const Icon(Icons.clear_rounded, size: 16),
                  onPressed: () => _refresh(() {
                    _nodeSearch.clear();
                    _nodeQuery = '';
                  }),
                )
              : null,
        ),
      );

  Widget _measureAllNodesButton() => OutlinedButton.icon(
        onPressed: _busy || !_connected ? null : _measureAllNodes,
        icon: const Icon(Icons.speed_rounded, size: 16),
        label: Text(_measuringNodes ? '测速中…' : '测试延迟'),
      );

  Widget _reloadNodesButton() => OutlinedButton.icon(
        onPressed: _busy ? null : (_connected ? _refreshNodes : _fetchNodes),
        icon: const Icon(Icons.refresh_rounded, size: 16),
        label: Text(_connected ? '刷新' : '获取节点'),
      );

  Widget _addNodeButton() => OutlinedButton.icon(
        onPressed: _busy ? null : () => unawaited(_showAddNodeDialog()),
        icon: const Icon(Icons.add_rounded, size: 16),
        label: const Text('添加节点'),
      );

  Widget _lockHint() => Text(
        _lockedRegion.isEmpty
            ? '选用节点后将自动锁定到其实测出口地区。'
            : '已锁定 $_lockedRegion，故障时在同地区候选内转移，不会跨地区直连。',
        style: Tokens.hint,
        overflow: TextOverflow.ellipsis,
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
