part of '../main.dart';

/// Exit-region verification, the locked region and the health of its candidate
/// pool. Everything the fail-closed behaviour depends on is visible here.
extension _RegionPage on _HomePageState {
  Widget _regionPage() {
    final job = _regionJob;
    final pool = _regionPool;
    final verified = _regionNodes.where((node) => node.usable).length;

    return _PageBody(children: [
      _SectionHeader(
        title: '地区锁定',
        trailing: Row(children: [
          _AkHoverButtonWrap(
            color: Tokens.cyan,
            child: TextButton.icon(
              onPressed: _busy || !_connected ? null : _patrolNow,
              icon: const Icon(Icons.monitor_heart_outlined, size: 16),
              label: const Text('立即巡检'),
            ),
          ),
          const SizedBox(width: 8),
          _AkHoverButtonWrap(
            color: Tokens.cyan,
            child: FilledButton.tonalIcon(
              onPressed: _busy || !_connected || job.running
                  ? null
                  : () => _verifyRegions(),
              icon: const Icon(Icons.public_rounded, size: 16),
              label: Text(job.running ? '验证中…' : '验证出口地区'),
            ),
          ),
        ]),
      ),
      Text(
        _lockedRegion.isEmpty
            ? '地区锁定以实测出口地区为准：逐个节点查询出口 IP 与地区，只有两个数据源一致的节点才会被采用。'
                '验证会依次切换节点，期间新连接可能短暂落到被验证的节点上。'
            : '已锁定地区 $_lockedRegion。节点只会在该地区的已验证候选内自动维护；'
                '候选耗尽时会阻断受保护流量，而不是跨地区直连。',
        style: Tokens.small,
      ),
      const SizedBox(height: 16),
      _Panel(
        highlighted: _lockedRegion.isNotEmpty,
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          _Stats(items: [
            ('锁定地区', _lockedRegion.isEmpty ? '未锁定' : _lockedRegion),
            ('候选节点', '${pool.length}'),
            ('已验证可用', '$verified / ${_regionNodes.length}'),
            ('当前出口',
                _activeRegion.isNotEmpty ? _activeRegion : (_connected ? '未知' : '—')),
            ('正常', '${_health?.count('HEALTHY') ?? 0}'),
            ('冷却中', '${_health?.count('COOLDOWN') ?? 0}'),
            ('不可用', '${_health?.count('UNHEALTHY') ?? 0}'),
          ]),
          if (job.running) ...[
            const SizedBox(height: 16),
            LinearProgressIndicator(
                value: job.total == 0 ? null : job.done / job.total),
            const SizedBox(height: 8),
            Text(
                '验证中 ${job.done}/${job.total}　已验证 ${job.verified}　跳过 ${job.skipped}　失败 ${job.failed}',
                style: Tokens.small),
          ],
          if (job.lastError.isNotEmpty) ...[
            const SizedBox(height: 14),
            _Notice(
                kind: _NoticeKind.warn, text: '上次验证未完成：${job.lastError}'),
          ],
          const SizedBox(height: 16),
          if (MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint) ...[
            Wrap(
              spacing: 8,
              runSpacing: 6,
              children: [
                OutlinedButton(
                    onPressed: _busy ? null : _showAllRegions,
                    child: const Text('全部地区')),
                OutlinedButton(
                    onPressed: _busy ? null : _showEvents,
                    child: const Text('切换事件')),
                OutlinedButton(
                    onPressed: _busy ? null : _showHealthParams,
                    child: const Text('健康策略')),
                if (_lockedRegion.isNotEmpty)
                  TextButton(
                      onPressed: _busy ? null : () => _lockRegion(''),
                      child: const Text('取消锁定')),
              ],
            ),
          ] else
            Row(children: [
              OutlinedButton(
                  onPressed: _busy ? null : _showAllRegions,
                  child: const Text('全部地区')),
              const SizedBox(width: 8),
              OutlinedButton(
                  onPressed: _busy ? null : _showEvents,
                  child: const Text('切换事件')),
              const SizedBox(width: 8),
              OutlinedButton(
                  onPressed: _busy ? null : _showHealthParams,
                  child: const Text('健康策略')),
              const Spacer(),
              if (_lockedRegion.isNotEmpty)
                TextButton(
                    onPressed: _busy ? null : () => _lockRegion(''),
                    child: const Text('取消锁定')),
            ]),
        ]),
      ),
      const SizedBox(height: 26),
      _SectionHeader(
        title: '锁定地区候选',
        trailing: pool.isEmpty
            ? null
            : Text('${pool.length} 个', style: Tokens.hint),
      ),
      if (pool.isEmpty)
        _Panel(
          child: _EmptyState(
            text: _lockedRegion.isEmpty
                ? '还没有候选节点。先验证出口地区，再到「节点」里选中一个带地区标记的节点。'
                : '锁定地区 $_lockedRegion 目前没有候选节点。可以重新验证，或取消锁定后改选其他地区。',
            actionLabel: _lockedRegion.isEmpty ? '去选节点' : '查看验证结果',
            onAction: _lockedRegion.isEmpty
                ? () => _goTo(_AppPage.nodes)
                : _showAllRegions,
          ),
        )
      else
        _Panel(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
          child: Column(children: [
            for (final name in pool) _candidateRow(name),
          ]),
        ),
    ]);
  }

  Widget _candidateRow(String name) {
    final health = _nodeHealth[name];
    final latency = health?.latencyMs ?? 0;
    final badge = _Badge(
        label: nodeHealthLabel(health?.state),
        color: nodeHealthDot(health?.state),
        tint: Tokens.idleTint);
    final inUse = name == _activeNode
        ? const _Badge(
            label: '使用中', color: Tokens.jade, tint: Tokens.jadeTint)
        : const SizedBox.shrink();
    final latencyLabel = Text(latency > 0 ? '$latency ms' : '—',
        textAlign: TextAlign.right, style: Tokens.number);

    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    final watermark = _lockedRegion.isNotEmpty ? _lockedRegion.toUpperCase() : 'CANDIDATE';
    final previewTag = latency > 0 ? '$latency MS' : 'VERIFIED';

    return _AkHoverRow(
      selected: name == _activeNode,
      accentColor: name == _activeNode ? Tokens.jade : Tokens.cyan,
      watermark: watermark,
      previewTag: previewTag,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
      child: narrow
          ? Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              _StatusDot(color: nodeHealthDot(health?.state)),
              const SizedBox(width: 10),
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(name,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(fontSize: 13)),
                  const SizedBox(height: 4),
                  Row(children: [
                    latencyLabel,
                    const SizedBox(width: 10),
                    badge,
                    if (name == _activeNode) ...[
                      const SizedBox(width: 8),
                      inUse,
                    ],
                  ]),
                ]),
              ),
            ])
          : Row(children: [
              _StatusDot(color: nodeHealthDot(health?.state)),
              const SizedBox(width: 10),
              Expanded(
                  child: Text(name,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(fontSize: 13))),
              SizedBox(width: 74, child: latencyLabel),
              const SizedBox(width: 14),
              SizedBox(
                width: 62,
                child: Align(alignment: Alignment.centerRight, child: badge),
              ),
              SizedBox(
                width: 58,
                child: Align(alignment: Alignment.centerRight, child: inUse),
              ),
            ]),
    );
  }
}
