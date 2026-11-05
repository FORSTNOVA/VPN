part of '../main.dart';

/// The Rhodes Island Protection & Region Policy (保护策略 / 地区与健康).
/// Governs exit-region verification, locked region policy, fail-closed isolation,
/// and automated health sweep patrols.
extension _RegionPage on _HomePageState {
  Widget _regionPage() {
    final job = _regionJob;
    final pool = _regionPool;
    final verified = _regionNodes.where((node) => node.usable).length;
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;

    return _PageBody(children: [
      _SectionHeader(
        title: '地区锁定',
        subtitle: 'REGION_LOCK',
        trailing: Wrap(
          spacing: 8,
          runSpacing: 6,
          children: [
            TextButton.icon(
              onPressed: _busy || !_connected ? null : _patrolNow,
              icon: const Icon(Icons.monitor_heart_outlined, size: 16),
              label: const Text('立即巡检'),
            ),
            FilledButton.icon(
              onPressed: _busy || !_connected || job.running
                  ? null
                  : () => _verifyRegions(),
              icon: const Icon(Icons.public_rounded, size: 16),
              label: Text(job.running ? '验证中…' : '验证出口地区'),
            ),
          ],
        ),
      ),
      Text(
        _lockedRegion.isEmpty
            ? '地区锁定以实测出口地区为准：逐个节点双源查询出口 IP 与地区，数据一致才会被采用。'
                '验证期间将依次切换节点，新连接可能短暂经过验证节点。'
            : '已锁定地区 $_lockedRegion。受保护流量仅在该地区已验证候选内自动切换；'
                '候选耗尽时阻断连接，坚决杜绝跨地区直连泄漏。',
        style: Tokens.small,
      ),
      const SizedBox(height: 14),

      // Protection Matrix Card
      _Panel(
        highlighted: _lockedRegion.isNotEmpty,
        accentColor: _lockedRegion.isNotEmpty ? Tokens.yellow : Tokens.cyan,
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            const Text('保护策略状态',
                style: TextStyle(fontSize: 14, fontWeight: FontWeight.w700)),
            const Spacer(),
            if (_lockedRegion.isNotEmpty)
              const _Badge(
                label: '已激活同地区容灾',
                color: Tokens.ok,
                tint: Tokens.okTint,
                showDot: true,
              )
            else
              const _Badge(
                label: '全局自动分流',
                color: Tokens.inkMuted,
                tint: Tokens.idleTint,
              ),
          ]),
          const SizedBox(height: 12),
          const Divider(),
          const SizedBox(height: 12),
          _Stats(items: [
            ('锁定地区', _lockedRegion.isEmpty ? '未锁定' : _lockedRegion),
            ('候选节点', '${pool.length}'),
            ('已验证可用', '$verified / ${_regionNodes.length}'),
            (
              '当前出口',
              _activeRegion.isNotEmpty
                  ? _activeRegion
                  : (_connected ? '未知' : '—')
            ),
            ('正常 (Healthy)', '${_health?.count('HEALTHY') ?? 0}'),
            ('冷却中 (Cooldown)', '${_health?.count('COOLDOWN') ?? 0}'),
            ('不可用 (Unhealthy)', '${_health?.count('UNHEALTHY') ?? 0}'),
          ]),
          if (job.running) ...[
            const SizedBox(height: 14),
            ClipRRect(
              borderRadius: BorderRadius.circular(2),
              child: LinearProgressIndicator(
                  value: job.total == 0 ? null : job.done / job.total),
            ),
            const SizedBox(height: 6),
            Text(
              '验证进度: ${job.done}/${job.total} · 已验证 ${job.verified} · 跳过 ${job.skipped} · 失败 ${job.failed}',
              style: Tokens.hint,
            ),
          ],
          if (job.lastError.isNotEmpty) ...[
            const SizedBox(height: 12),
            _Notice(kind: _NoticeKind.warn, text: '上次验证未完成：${job.lastError}'),
          ],
          const SizedBox(height: 14),
          const Divider(),
          const SizedBox(height: 10),
          Wrap(
            spacing: 8,
            runSpacing: 6,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              OutlinedButton(
                onPressed: _busy ? null : _showAllRegions,
                child: const Text('全部地区'),
              ),
              OutlinedButton(
                onPressed: _busy ? null : _showEvents,
                child: const Text('切换事件'),
              ),
              OutlinedButton(
                onPressed: _busy ? null : _showHealthParams,
                child: const Text('健康策略'),
              ),
              if (_lockedRegion.isNotEmpty)
                TextButton(
                  onPressed: _busy ? null : () => _lockRegion(''),
                  child: const Text('取消锁定'),
                ),
            ],
          ),
        ]),
      ),

      const SizedBox(height: 22),

      // Candidate Nodes Section
      _SectionHeader(
        title: '锁定地区候选',
        subtitle: 'CANDIDATE_POOL',
        trailing: pool.isEmpty
            ? null
            : Text('共 ${pool.length} 个候选节点', style: Tokens.hint),
      ),
      if (pool.isEmpty)
        _Panel(
          child: _EmptyState(
            text: _lockedRegion.isEmpty
                ? '暂无地区候选池。先点击「验证出口地区」，或前往「线路」选择带地区标记的节点。'
                : '锁定地区 $_lockedRegion 当前暂无可用的已验证节点。可尝试重新验证或改选其他地区。',
            actionLabel: _lockedRegion.isEmpty ? '前往线路选择' : '查看全部验证结果',
            onAction: _lockedRegion.isEmpty
                ? () => _goTo(_AppPage.nodes)
                : _showAllRegions,
          ),
        )
      else
        _Panel(
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
          child: Column(children: [
            for (final name in pool) _candidateRow(name, narrow: narrow),
          ]),
        ),
    ]);
  }

  Widget _candidateRow(String name, {required bool narrow}) {
    final health = _nodeHealth[name];
    final latency = health?.latencyMs ?? 0;
    final isActive = name == _activeNode;

    final badge = _Badge(
      label: nodeHealthLabel(health?.state),
      color: nodeHealthDot(health?.state),
      tint: Tokens.idleTint,
    );
    final inUse = isActive
        ? const _Badge(
            label: '使用中',
            color: Tokens.ok,
            tint: Tokens.okTint,
            showDot: true,
          )
        : const SizedBox.shrink();

    final latencyLabel = Text(
      latency > 0 ? '$latency ms' : '—',
      textAlign: TextAlign.right,
      style: Tokens.number.copyWith(
        color: latency > 0 ? Tokens.cyan : Tokens.inkFaint,
        fontWeight: FontWeight.w700,
      ),
    );

    return _AkHoverRow(
      selected: isActive,
      accentColor: isActive ? Tokens.ok : Tokens.cyan,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
      child: narrow
          ? Row(crossAxisAlignment: CrossAxisAlignment.center, children: [
              _StatusDot(color: nodeHealthDot(health?.state), size: 8),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      name,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        fontSize: 13,
                        fontWeight:
                            isActive ? FontWeight.w700 : FontWeight.w500,
                        color: isActive ? Tokens.ok : Tokens.ink,
                      ),
                    ),
                    const SizedBox(height: 3),
                    Row(children: [
                      latencyLabel,
                      const SizedBox(width: 8),
                      badge,
                      if (isActive) ...[
                        const SizedBox(width: 6),
                        inUse,
                      ],
                    ]),
                  ],
                ),
              ),
            ])
          : Row(children: [
              _StatusDot(color: nodeHealthDot(health?.state), size: 8),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  name,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontSize: 13,
                    fontWeight: isActive ? FontWeight.w700 : FontWeight.w500,
                    color: isActive ? Tokens.ok : Tokens.ink,
                  ),
                ),
              ),
              SizedBox(width: 76, child: latencyLabel),
              const SizedBox(width: 10),
              SizedBox(
                width: 68,
                child: Align(alignment: Alignment.centerRight, child: badge),
              ),
              const SizedBox(width: 6),
              SizedBox(
                width: 64,
                child: Align(alignment: Alignment.centerRight, child: inUse),
              ),
            ]),
    );
  }
}
