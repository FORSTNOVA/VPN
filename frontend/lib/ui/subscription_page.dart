part of '../main.dart';

/// The Rhodes Island Subscription & Kernel Operations (订阅 / 内核).
/// Manages multi-subscription merge, single-source quota, manual overrides,
/// and secondary advanced kernel/TUN environments.
extension _SubscriptionPage on _HomePageState {
  Widget _subscriptionPage() {
    final tun = _tun;
    final wintunInstalled = tun?.wintun.installed ?? false;
    final enabledCount = _savedSubscriptions.where((s) => s.enabled).length;

    return _PageBody(children: [
      // 1. Multi-Subscription Management & Merge
      _SectionHeader(
        title: '多订阅管理与合并',
        subtitle: 'PROFILES_MERGE',
        trailing: Wrap(
          spacing: 8,
          runSpacing: 6,
          children: [
            OutlinedButton.icon(
              onPressed:
                  _busy || _connected ? null : _showAddSubscriptionDialog,
              icon: const Icon(Icons.add_rounded, size: 16),
              label: const Text('添加订阅'),
            ),
            if (!_isMergedSubscription && enabledCount > 0)
              OutlinedButton.icon(
                onPressed:
                    _busy || _connected ? null : _activateMergedSubscription,
                icon: const Icon(Icons.hub_rounded, size: 16),
                label: const Text('切回合并主源'),
              ),
            FilledButton.icon(
              onPressed: (_busy || _connected || enabledCount == 0)
                  ? null
                  : _mergeAllSubscriptions,
              icon: const Icon(Icons.merge_type_rounded, size: 16),
              label: Text('一键合并更新 ($enabledCount)'),
            ),
          ],
        ),
      ),

      if (_subscriptionsFromCache) ...[
        const _Notice(
          kind: _NoticeKind.warn,
          text: '当前用的是缓存副本：上次拉取失败时启用了最后一份可用配置，节点可能已经变化。'
              '恢复网络后点击「一键合并更新」即可重新拉取。',
        ),
        const SizedBox(height: 12),
      ],

      if (_savedSubscriptions.isEmpty)
        _Panel(
          child: _EmptyState(
            text: '尚未保存任何订阅源。录入您的节点订阅后可单独使用或多源合并。',
            actionLabel: '添加首个订阅',
            onAction: _showAddSubscriptionDialog,
          ),
        )
      else
        _Panel(
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(
              children: [
                Expanded(
                  child: Row(
                    children: [
                      const Text('已保存的订阅源',
                          style: TextStyle(
                              fontSize: 14, fontWeight: FontWeight.w700)),
                      const SizedBox(width: 8),
                      if (_isMergedSubscription)
                        const _Badge(
                          label: '当前运行：已合并主源',
                          color: Tokens.ok,
                          tint: Tokens.okTint,
                          showDot: true,
                        ),
                    ],
                  ),
                ),
                Text(
                  '共 ${_savedSubscriptions.length} 个源 · 已启用 $enabledCount 个用于合并',
                  style: Tokens.hint,
                ),
              ],
            ),
            const SizedBox(height: 10),
            const Divider(),
            const SizedBox(height: 6),
            for (final entry in _savedSubscriptions) _subscriptionRow(entry),
            const SizedBox(height: 10),
            Row(
              mainAxisAlignment: MainAxisAlignment.end,
              children: [
                const Icon(Icons.schedule_rounded,
                    size: 15, color: Tokens.inkMuted),
                const SizedBox(width: 6),
                const Text('定时自动合并: ', style: Tokens.small),
                DropdownButton<int>(
                  value: _autoMergeHours,
                  isDense: true,
                  underline: const SizedBox(),
                  dropdownColor: Tokens.surfaceElevated,
                  items: const [
                    DropdownMenuItem(
                        value: 0, child: Text('关闭', style: Tokens.small)),
                    DropdownMenuItem(
                        value: 6, child: Text('每 6 小时', style: Tokens.small)),
                    DropdownMenuItem(
                        value: 12, child: Text('每 12 小时', style: Tokens.small)),
                    DropdownMenuItem(
                        value: 24, child: Text('每 24 小时', style: Tokens.small)),
                  ],
                  onChanged: _busy
                      ? null
                      : (v) => unawaited(_setAutoMergeHours(v ?? 0)),
                ),
              ],
            ),
          ]),
        ),

      const SizedBox(height: 22),

      // 2. Direct Subscription / Quick Single Source
      const _SectionHeader(
        title: '快速直连录入',
        subtitle: 'DIRECT_SOURCE',
      ),
      _Panel(
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          TextField(
            controller: _subscription,
            enabled: !_connected && !_busy,
            decoration: InputDecoration(
              labelText: '订阅地址',
              hintText: _isMergedSubscription
                  ? '当前正在使用多订阅合并；输入新链接保存将切换为单源直连'
                  : 'https://… 或多个链接用 | 分隔，或直接粘贴 hysteria2:// 等节点',
              prefixIcon: const Icon(Icons.link_rounded, size: 18),
            ),
          ),
          const SizedBox(height: 12),
          Wrap(
            spacing: 10,
            runSpacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              FilledButton(
                onPressed: _busy || _connected
                    ? null
                    : () => unawaited(_saveSettings()),
                child: const Text('保存单源'),
              ),
              OutlinedButton.icon(
                onPressed:
                    _busy ? null : (_connected ? _refreshNodes : _fetchNodes),
                icon: const Icon(Icons.cloud_download_outlined, size: 16),
                label: Text(_hasFetchedNodes ? '重新获取节点' : '获取节点'),
              ),
              OutlinedButton.icon(
                onPressed: _busy ? null : _showDiagnostics,
                icon: const Icon(Icons.analytics_outlined, size: 16),
                label: const Text('订阅诊断'),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Text(
            _connected
                ? '已连接，断开后才能修改或切换主订阅。'
                : '内置本地转换引擎支持 Clash YAML、Sing-Box JSON、V2Ray/Base64、SSR、TUIC、Hysteria2 等协议。',
            style: Tokens.hint,
          ),
        ]),
      ),

      const SizedBox(height: 22),

      // 3. Secondary Advanced Kernel & Environment Settings (Progressive Disclosure)
      _Panel(
        padding: EdgeInsets.zero,
        child: ExpansionTile(
            title: const Text('内核与底层环境'),
            subtitle: const Text('程序路径、权限与 TUN 配置'),
            childrenPadding: const EdgeInsets.fromLTRB(18, 0, 18, 18),
            expandedCrossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (Platform.isAndroid) ...[
                const Text(
                  '内核随应用原生编译打包，没有外置路径需要配置；底层虚拟网卡由 Android VpnService 统一管理。',
                  style: Tokens.small,
                ),
                const SizedBox(height: 12),
                const Divider(),
                const SizedBox(height: 10),
                _KeyValueRow(
                  label: '配置目录',
                  value: profileLabel(home: _homePath, portable: _portable),
                ),
                _KeyValueRow(
                  label: 'VPN 授权',
                  value: _elevated ? '已就绪' : '未授权',
                  valueColor: _elevated ? Tokens.ok : Tokens.warn,
                ),
              ] else ...[
                TextField(
                  controller: _mihomo,
                  enabled: !_connected && !_busy,
                  decoration: InputDecoration(
                    labelText: 'Mihomo 程序路径',
                    hintText: _mihomoResolved.isEmpty
                        ? '留空则使用配置目录下的 mihomo.exe'
                        : '留空则使用 $_mihomoResolved',
                    prefixIcon: const Icon(Icons.memory_rounded, size: 18),
                  ),
                ),
                const SizedBox(height: 16),
                Row(children: [
                  Icon(
                    wintunInstalled
                        ? Icons.check_circle_rounded
                        : Icons.error_outline_rounded,
                    size: 18,
                    color: wintunInstalled ? Tokens.ok : Tokens.bad,
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        const Text('wintun.dll',
                            style: TextStyle(
                                fontSize: 13, fontWeight: FontWeight.w700)),
                        Text(
                          wintunInstalled
                              ? '${tun?.wintun.version ?? ''}　${tun?.wintun.path ?? ''}'
                              : '尚未安装。TUN 模式需要此驱动，下载时自动校验 WireGuard 官方哈希与数字签名。',
                          style: Tokens.hint,
                        ),
                      ],
                    ),
                  ),
                  OutlinedButton(
                    onPressed: _busy ? null : _downloadWintun,
                    child: Text(wintunInstalled ? '重新安装' : '下载 wintun.dll'),
                  ),
                ]),
                const SizedBox(height: 12),
                TextField(
                  controller: _wintun,
                  enabled: !_connected && !_busy,
                  decoration: const InputDecoration(
                    labelText: 'wintun.dll 自定义路径（留空则自动定位）',
                    prefixIcon: Icon(Icons.folder_outlined, size: 18),
                  ),
                ),
                const SizedBox(height: 16),
                const Divider(),
                const SizedBox(height: 10),
                _KeyValueRow(
                    label: '虚拟网卡', value: tun?.interface ?? 'SmartVPN'),
                _KeyValueRow(label: 'TUN 栈架构', value: tun?.stack ?? 'gvisor'),
                _KeyValueRow(label: 'MTU', value: '${tun?.mtu ?? 1500}'),
                _KeyValueRow(
                  label: '配置目录',
                  value: profileLabel(home: _homePath, portable: _portable),
                ),
                _KeyValueRow(
                  label: '管理员权限',
                  value: _elevated ? '已取得' : '未取得',
                  valueColor: _elevated ? Tokens.ok : Tokens.warn,
                  trailing: _elevated
                      ? null
                      : TextButton(
                          onPressed: _busy ? null : _relaunchElevated,
                          child: const Text('以管理员身份重启'),
                        ),
                ),
              ],
            ]),
      ),
    ]);
  }

  /// One saved subscription card with enable checkbox, quota progress bar and operations.
  Widget _subscriptionRow(SavedSubscription entry) {
    final isPartInMerged = _isMergedSubscription && entry.enabled;
    final active = entry.url == _savedSubscriptionUrl;
    final blocked = _busy || _kernelRunning;
    final uinfo = entry.userInfo;
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;

    return Container(
      margin: const EdgeInsets.symmetric(vertical: 6),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Tokens.surfaceElevated,
        borderRadius: BorderRadius.circular(Tokens.radiusPanel),
        border: Border.all(
          color: active
              ? Tokens.ok.withValues(alpha: 0.65)
              : (isPartInMerged
                  ? Tokens.yellow.withValues(alpha: 0.5)
                  : Tokens.hairline),
          width: (active || isPartInMerged) ? 1.5 : 1,
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(crossAxisAlignment: CrossAxisAlignment.center, children: [
            Checkbox(
              value: entry.enabled,
              onChanged: blocked
                  ? null
                  : (v) =>
                      unawaited(_toggleSubscriptionEnabled(entry, v ?? false)),
            ),
            const SizedBox(width: 4),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Wrap(
                    spacing: 6,
                    runSpacing: 4,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [
                      Text(
                        entry.label,
                        style: const TextStyle(
                            fontSize: 13, fontWeight: FontWeight.w700),
                      ),
                      if (entry.prefix.isNotEmpty)
                        _Badge(
                          label: entry.prefix,
                          color: Tokens.yellow,
                          tint: Tokens.yellowTint,
                        ),
                      if (active)
                        const _Badge(
                          label: '当前主源 (单用)',
                          color: Tokens.ok,
                          tint: Tokens.okTint,
                          showDot: true,
                        )
                      else if (isPartInMerged)
                        const _Badge(
                          label: '已并入主源',
                          color: Tokens.yellow,
                          tint: Tokens.yellowTint,
                          showDot: true,
                        ),
                    ],
                  ),
                  const SizedBox(height: 3),
                  Text(
                    subscriptionDetail(entry),
                    style: entry.hasFailed
                        ? const TextStyle(fontSize: 11.5, color: Tokens.warn)
                        : Tokens.hint,
                  ),
                ],
              ),
            ),
            if (!narrow) ...[
              IconButton(
                tooltip: '单独拉取刷新',
                icon: const Icon(Icons.refresh_rounded, size: 18),
                onPressed: blocked
                    ? null
                    : () => unawaited(_refreshSingleSubscription(entry)),
              ),
              IconButton(
                tooltip: '刷新剩余流量',
                icon: _quotaRefreshingId == entry.id
                    ? const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : const Icon(Icons.data_usage_rounded, size: 18),
                onPressed: (_busy || _quotaRefreshingId != null)
                    ? null
                    : () => unawaited(_refreshSubscriptionQuota(entry)),
              ),
              if (!active)
                TextButton(
                  onPressed: blocked
                      ? null
                      : () => unawaited(_activateSubscription(entry)),
                  child: const Text('单用'),
                ),
              TextButton(
                onPressed:
                    blocked ? null : () => unawaited(_editSubscription(entry)),
                child: const Text('编辑'),
              ),
              TextButton(
                onPressed: blocked
                    ? null
                    : () => unawaited(_deleteSubscription(entry)),
                child: const Text('删除'),
              ),
            ],
          ]),

          // Quota Bar
          if (uinfo != null) ...[
            const SizedBox(height: 8),
            Padding(
              padding: const EdgeInsets.only(left: 44, right: 8),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  ClipRRect(
                    borderRadius: BorderRadius.circular(2),
                    child: LinearProgressIndicator(
                      value: uinfo.progress,
                      minHeight: 4,
                      backgroundColor: Tokens.hairline,
                      valueColor: AlwaysStoppedAnimation<Color>(
                        uinfo.progress > 0.9 ? Tokens.warn : Tokens.cyan,
                      ),
                    ),
                  ),
                  const SizedBox(height: 4),
                  Wrap(
                    spacing: 16,
                    runSpacing: 4,
                    children: [
                      Text(
                        '已用: ${uinfo.usedString} / 总量: ${uinfo.totalString}',
                        style: Tokens.small,
                      ),
                      Text(
                        '到期: ${uinfo.expireString}',
                        style: Tokens.small,
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ],

          const SizedBox(height: 8),
          Text(
            entry.quotaError.isNotEmpty
                ? '用量刷新失败：${entry.quotaError}'
                : uinfo == null
                    ? '暂无用量信息，可点击用量刷新。'
                    : '用量更新于 ${updatedLabel(entry.quotaUpdatedAt > 0 ? entry.quotaUpdatedAt : entry.updatedAt)}',
            style: entry.quotaError.isNotEmpty
                ? Tokens.hint.copyWith(color: Tokens.warn)
                : Tokens.hint,
          ),

          if (narrow) ...[
            const SizedBox(height: 8),
            Padding(
              padding: const EdgeInsets.only(left: 36),
              child: Wrap(
                spacing: 4,
                runSpacing: 4,
                children: [
                  TextButton.icon(
                    onPressed: blocked
                        ? null
                        : () => unawaited(_refreshSingleSubscription(entry)),
                    icon: const Icon(Icons.refresh_rounded, size: 14),
                    label: const Text('拉取'),
                  ),
                  TextButton.icon(
                    onPressed: (_busy || _quotaRefreshingId != null)
                        ? null
                        : () => unawaited(_refreshSubscriptionQuota(entry)),
                    icon: const Icon(Icons.data_usage_rounded, size: 14),
                    label: const Text('用量'),
                  ),
                  if (!active)
                    TextButton.icon(
                      onPressed: blocked
                          ? null
                          : () => unawaited(_activateSubscription(entry)),
                      icon: const Icon(Icons.play_arrow_rounded, size: 14),
                      label: const Text('单用'),
                    ),
                  TextButton.icon(
                    onPressed: blocked
                        ? null
                        : () => unawaited(_editSubscription(entry)),
                    icon: const Icon(Icons.edit_outlined, size: 14),
                    label: const Text('编辑'),
                  ),
                  TextButton.icon(
                    onPressed: blocked
                        ? null
                        : () => unawaited(_deleteSubscription(entry)),
                    icon: const Icon(Icons.delete_outline_rounded, size: 14),
                    label: const Text('删除'),
                  ),
                ],
              ),
            ),
          ],
        ],
      ),
    );
  }
}
