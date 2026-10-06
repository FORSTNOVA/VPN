part of '../main.dart';

/// Subscription and kernel configuration: where the nodes come from, which
/// Mihomo binary runs, and what TUN needs on disk.
extension _SubscriptionPage on _HomePageState {
  Widget _subscriptionPage() {
    final tun = _tun;
    final wintunInstalled = tun?.wintun.installed ?? false;
    final enabledCount =
        _savedSubscriptions.where((s) => s.enabled).length;

    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;

    return _PageBody(children: [
      const _SectionHeader(title: '多订阅管理与合并'),
      if (_savedSubscriptions.isNotEmpty) ...[
        _Panel(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            if (narrow) ...[
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  const Text('已保存的订阅',
                      style: TextStyle(
                          fontSize: 14, fontWeight: FontWeight.w600)),
                  if (_isMergedSubscription)
                    const _Badge(
                      label: '当前主源：已合并',
                      color: Tokens.ok,
                      tint: Tokens.okTint,
                    ),
                ],
              ),
              const SizedBox(height: 3),
              Text(
                '共 ${_savedSubscriptions.length} 个源 · 已启用 $enabledCount 个用于合并',
                style: Tokens.hint,
              ),
              const SizedBox(height: 10),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  OutlinedButton.icon(
                    onPressed: _busy || _connected
                        ? null
                        : _showAddSubscriptionDialog,
                    icon: const Icon(Icons.add_rounded, size: 16),
                    label: const Text('添加订阅'),
                  ),
                  if (!_isMergedSubscription && enabledCount > 0)
                    OutlinedButton.icon(
                      onPressed: _busy || _connected
                          ? null
                          : _activateMergedSubscription,
                      icon: const Icon(Icons.hub_rounded, size: 16),
                      label: const Text('切回合并主源'),
                    ),
                  _AkHoverButtonWrap(
                    color: Tokens.cyan,
                    child: FilledButton.icon(
                      onPressed: (_busy || _connected || enabledCount == 0)
                          ? null
                          : _mergeAllSubscriptions,
                      icon: const Icon(Icons.merge_type_rounded, size: 16),
                      label: Text('一键合并更新 ($enabledCount)'),
                    ),
                  ),
                ],
              ),
            ] else ...[
              Row(
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(children: [
                          const Text('已保存的订阅',
                              style: TextStyle(
                                  fontSize: 14, fontWeight: FontWeight.w600)),
                          const SizedBox(width: 8),
                          if (_isMergedSubscription)
                            const _Badge(
                              label: '当前主源：已合并',
                              color: Tokens.ok,
                              tint: Tokens.okTint,
                            ),
                        ]),
                        const SizedBox(height: 2),
                        Text(
                          '共 ${_savedSubscriptions.length} 个源 · 已启用 $enabledCount 个用于合并',
                          style: Tokens.hint,
                        ),
                      ],
                    ),
                  ),
                  Wrap(
                    spacing: 8,
                    runSpacing: 6,
                    children: [
                      OutlinedButton.icon(
                        onPressed: _busy || _connected
                            ? null
                            : _showAddSubscriptionDialog,
                        icon: const Icon(Icons.add_rounded, size: 16),
                        label: const Text('添加订阅'),
                      ),
                      if (!_isMergedSubscription && enabledCount > 0)
                        OutlinedButton.icon(
                          onPressed: _busy || _connected
                              ? null
                              : _activateMergedSubscription,
                          icon: const Icon(Icons.hub_rounded, size: 16),
                          label: const Text('切回合并主源'),
                        ),
                      _AkHoverButtonWrap(
                        color: Tokens.cyan,
                        child: FilledButton.icon(
                          onPressed: (_busy || _connected || enabledCount == 0)
                              ? null
                              : _mergeAllSubscriptions,
                          icon: const Icon(Icons.merge_type_rounded, size: 16),
                          label: Text('一键合并更新 ($enabledCount)'),
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ],
            const SizedBox(height: 12),
            const Divider(),
            const SizedBox(height: 6),
            for (final entry in _savedSubscriptions) _subscriptionRow(entry),
            const SizedBox(height: 8),
            Row(
              mainAxisAlignment: MainAxisAlignment.end,
              children: [
                const Icon(Icons.schedule_rounded, size: 15, color: Tokens.inkMuted),
                const SizedBox(width: 6),
                const Text('定时自动合并: ', style: Tokens.small),
                DropdownButton<int>(
                  value: _autoMergeHours,
                  isDense: true,
                  underline: const SizedBox(),
                  dropdownColor: Tokens.surfaceElevated,
                  items: const [
                    DropdownMenuItem(value: 0, child: Text('关闭', style: Tokens.small)),
                    DropdownMenuItem(value: 6, child: Text('每 6 小时', style: Tokens.small)),
                    DropdownMenuItem(value: 12, child: Text('每 12 小时', style: Tokens.small)),
                    DropdownMenuItem(value: 24, child: Text('每 24 小时', style: Tokens.small)),
                  ],
                  onChanged: _busy ? null : (v) => unawaited(_setAutoMergeHours(v ?? 0)),
                ),
              ],
            ),
          ]),
        ),
        const SizedBox(height: 14),
      ],
      if (_subscriptionsFromCache) ...[
        const _Notice(
          kind: _NoticeKind.warn,
          text: '当前用的是缓存副本：上次拉取失败时启用了最后一份可用配置，节点可能已经变化。'
              '恢复网络后点「一键合并更新」即可重新拉取。',
        ),
        const SizedBox(height: 14),
      ],
      _Panel(
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              const Text('快速录入 / 单源直连',
                  style: TextStyle(fontSize: 13, fontWeight: FontWeight.w600)),
              if (_savedSubscriptions.isEmpty)
                TextButton.icon(
                  onPressed: _busy || _connected
                      ? null
                      : _showAddSubscriptionDialog,
                  icon: const Icon(Icons.add_rounded, size: 16),
                  label: const Text('添加至列表'),
                ),
            ],
          ),
          const SizedBox(height: 8),
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
            spacing: 8,
            runSpacing: 6,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              FilledButton(
                onPressed: _busy || _connected
                    ? null
                    : () => unawaited(_saveSettings()),
                child: const Text('保存'),
              ),
              _AkHoverButtonWrap(
                color: Tokens.cyan,
                child: OutlinedButton.icon(
                  onPressed: _busy
                      ? null
                      : (_connected ? _refreshNodes : _fetchNodes),
                  icon: const Icon(Icons.cloud_download_outlined, size: 16),
                  label: Text(_hasFetchedNodes ? '重新获取节点' : '获取节点'),
                ),
              ),
              _AkHoverButtonWrap(
                color: Tokens.cyan,
                child: TextButton.icon(
                  onPressed: _busy ? null : _showDiagnostics,
                  icon: const Icon(Icons.analytics_outlined, size: 16),
                  label: const Text('订阅诊断'),
                ),
              ),
            ],
          ),
          const SizedBox(height: 10),
          Text(
            _connected
                ? '已连接，断开后才能修改或切换订阅。'
                : '内置本地转换引擎全面支持 Clash YAML、Sing-Box JSON、V2Ray/Base64、SSR、TUIC、Hysteria2 等协议。'
                    '多订阅合并时会自动去重并按自定义前缀组织节点名。',
            style: Tokens.hint,
          ),
        ]),
      ),
      const SizedBox(height: 22),
      if (Platform.isAndroid) ...[
        const _SectionHeader(title: '内核'),
        _Panel(
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            const Text('内核随应用一起打包，没有路径需要填写；隧道由系统的 VPN 服务建立。',
                style: Tokens.small),
            const SizedBox(height: 14),
            const Divider(),
            const SizedBox(height: 10),
            _KeyValueRow(
                label: '配置目录',
                value: profileLabel(home: _homePath, portable: _portable)),
            _KeyValueRow(
              label: 'VPN 授权',
              value: _elevated ? '已获得' : '未获得',
              valueColor: _elevated ? Tokens.ok : Tokens.warn,
            ),
          ]),
        ),
      ] else ...[
        const _SectionHeader(title: '内核'),
        _Panel(
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            TextField(
              controller: _mihomo,
              enabled: !_connected && !_busy,
              decoration: InputDecoration(
                labelText: 'Mihomo 程序路径',
                hintText: _mihomoResolved.isEmpty
                    ? '留空则用配置目录下的 mihomo.exe'
                    : '留空则用 $_mihomoResolved',
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
                  color: wintunInstalled ? Tokens.ok : Tokens.bad),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const Text('wintun.dll',
                          style: TextStyle(
                              fontSize: 13, fontWeight: FontWeight.w600)),
                      Text(
                        wintunInstalled
                            ? '${tun?.wintun.version ?? ''}　${tun?.wintun.path ?? ''}'
                            : '尚未安装。TUN 模式需要它，下载时会校验哈希与 WireGuard 的数字签名。',
                        style: Tokens.hint,
                      ),
                    ]),
              ),
              TextButton(
                onPressed: _busy ? null : _downloadWintun,
                child: Text(wintunInstalled ? '重新安装' : '下载 wintun.dll'),
              ),
            ]),
            const SizedBox(height: 12),
            TextField(
              controller: _wintun,
              enabled: !_connected && !_busy,
              decoration: const InputDecoration(
                labelText: 'wintun.dll 路径（留空则自动下载）',
                prefixIcon: Icon(Icons.folder_outlined, size: 18),
              ),
            ),
            const SizedBox(height: 18),
            const Divider(),
            const SizedBox(height: 10),
            _KeyValueRow(label: '虚拟网卡', value: tun?.interface ?? 'SmartVPN'),
            _KeyValueRow(label: 'TUN 栈', value: tun?.stack ?? 'gvisor'),
            _KeyValueRow(label: 'MTU', value: '${tun?.mtu ?? 1500}'),
            _KeyValueRow(
                label: '配置目录',
                value: profileLabel(home: _homePath, portable: _portable)),
            _KeyValueRow(
              label: '管理员权限',
              value: _elevated ? '已获得' : '未获得',
              valueColor: _elevated ? Tokens.ok : Tokens.warn,
              trailing: _elevated
                  ? null
                  : TextButton(
                      onPressed: _busy ? null : _relaunchElevated,
                      child: const Text('以管理员身份重启')),
            ),
          ]),
        ),
      ],
    ]);
  }

  /// One saved subscription card/row with enable checkbox, badges, traffic progress and operations
  Widget _subscriptionRow(SavedSubscription entry) {
    final isPartInMerged = _isMergedSubscription && entry.enabled;
    final active = entry.url == _savedSubscriptionUrl;
    final blocked = _busy || _kernelRunning;
    final uinfo = entry.userInfo;
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;

    return Container(
      margin: const EdgeInsets.symmetric(vertical: 6),
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: Tokens.surfaceElevated,
        borderRadius: BorderRadius.circular(8),
        border: Border.all(
          color: active
              ? Tokens.ok.withOpacity(0.6)
              : (isPartInMerged
                  ? Tokens.cyan.withOpacity(0.5)
                  : Tokens.hairline),
          width: (active || isPartInMerged) ? 1.5 : 1,
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (narrow) ...[
            // Narrow (Mobile) layout
            Row(children: [
              Checkbox(
                value: entry.enabled,
                onChanged: blocked
                    ? null
                    : (v) => unawaited(
                        _toggleSubscriptionEnabled(entry, v ?? false)),
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
                              fontSize: 13, fontWeight: FontWeight.w600),
                        ),
                        if (entry.prefix.isNotEmpty)
                          _Badge(
                            label: entry.prefix,
                            color: Tokens.cyan,
                            tint: Tokens.cyanTint,
                          ),
                        if (active)
                          const _Badge(
                              label: '当前主源 (单用)',
                              color: Tokens.ok,
                              tint: Tokens.okTint)
                        else if (isPartInMerged)
                          const _Badge(
                              label: '已启用来源',
                              color: Tokens.ok,
                              tint: Tokens.okTint),
                      ],
                    ),
                  ],
                ),
              ),
              IconButton(
                tooltip: '单独拉取刷新',
                icon: const Icon(Icons.refresh_rounded, size: 18),
                onPressed: blocked
                    ? null
                    : () => unawaited(_refreshSingleSubscription(entry)),
              ),
            ]),
            Padding(
              padding: const EdgeInsets.only(left: 44, right: 8, bottom: 4),
              child: Text(
                subscriptionDetail(entry),
                style: entry.hasFailed
                    ? const TextStyle(fontSize: 12, color: Tokens.warn)
                    : Tokens.hint,
              ),
            ),
            if (uinfo == null)
              Padding(
                padding: const EdgeInsets.only(left: 44, right: 8, bottom: 4),
                child: Text(
                  entry.quotaError.isNotEmpty
                      ? '流量刷新失败：${entry.quotaError}'
                      : '暂无流量信息，可点击“刷新用量”获取',
                  style: entry.quotaError.isNotEmpty
                      ? const TextStyle(fontSize: 12, color: Tokens.warn)
                      : Tokens.hint,
                ),
              ),
            if (uinfo != null) ...[
              Padding(
                padding: const EdgeInsets.only(
                    left: 44, right: 8, top: 4, bottom: 6),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    ClipRRect(
                      borderRadius: BorderRadius.circular(3),
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
                      alignment: WrapAlignment.spaceBetween,
                      crossAxisAlignment: WrapCrossAlignment.center,
                      spacing: 8,
                      runSpacing: 2,
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
                    const SizedBox(height: 3),
                    Text(
                      entry.quotaError.isNotEmpty
                          ? '刷新失败：${entry.quotaError} · 上次更新 ${updatedLabel(entry.quotaUpdatedAt > 0 ? entry.quotaUpdatedAt : entry.updatedAt)}'
                          : '用量更新于 ${updatedLabel(entry.quotaUpdatedAt > 0 ? entry.quotaUpdatedAt : entry.updatedAt)}',
                      style: entry.quotaError.isNotEmpty
                          ? const TextStyle(fontSize: 11, color: Tokens.warn)
                          : Tokens.hint,
                    ),
                  ],
                ),
              ),
            ],
            Padding(
              padding: const EdgeInsets.only(top: 2),
              child: Wrap(
                alignment: WrapAlignment.end,
                spacing: 4,
                runSpacing: 4,
                children: [
                  if (!active)
                    TextButton.icon(
                      onPressed: blocked
                          ? null
                          : () => unawaited(_activateSubscription(entry)),
                      icon: const Icon(Icons.play_arrow_rounded, size: 15),
                      label: const Text('单用'),
                      style: TextButton.styleFrom(
                        visualDensity: VisualDensity.compact,
                        padding: const EdgeInsets.symmetric(
                            horizontal: 8, vertical: 4),
                      ),
                    ),
                  TextButton.icon(
                    onPressed: (_busy || _quotaRefreshingId != null)
                        ? null
                        : () => unawaited(_refreshSubscriptionQuota(entry)),
                    icon: _quotaRefreshingId == entry.id
                        ? const SizedBox(
                            width: 14,
                            height: 14,
                            child: CircularProgressIndicator(strokeWidth: 2),
                          )
                        : const Icon(Icons.data_usage_rounded, size: 14),
                    label: const Text('刷新用量'),
                    style: TextButton.styleFrom(
                      visualDensity: VisualDensity.compact,
                      padding: const EdgeInsets.symmetric(
                          horizontal: 8, vertical: 4),
                    ),
                  ),
                  TextButton.icon(
                    onPressed: blocked
                        ? null
                        : () => unawaited(_editSubscription(entry)),
                    icon: const Icon(Icons.edit_outlined, size: 14),
                    label: const Text('编辑'),
                    style: TextButton.styleFrom(
                      visualDensity: VisualDensity.compact,
                      padding: const EdgeInsets.symmetric(
                          horizontal: 8, vertical: 4),
                    ),
                  ),
                  TextButton.icon(
                    onPressed: blocked
                        ? null
                        : () => unawaited(_deleteSubscription(entry)),
                    icon: const Icon(Icons.delete_outline_rounded, size: 14),
                    style: TextButton.styleFrom(
                      foregroundColor: Tokens.inkMuted,
                      visualDensity: VisualDensity.compact,
                      padding: const EdgeInsets.symmetric(
                          horizontal: 8, vertical: 4),
                    ),
                    label: const Text('删除'),
                  ),
                ],
              ),
            ),
          ] else ...[
            // Desktop wide layout
            Row(children: [
              Checkbox(
                value: entry.enabled,
                onChanged: blocked
                    ? null
                    : (v) => unawaited(
                        _toggleSubscriptionEnabled(entry, v ?? false)),
              ),
              const SizedBox(width: 4),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(children: [
                      Flexible(
                        child: Text(entry.label,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(
                                fontSize: 13, fontWeight: FontWeight.w600)),
                      ),
                      if (entry.prefix.isNotEmpty) ...[
                        const SizedBox(width: 6),
                        _Badge(
                          label: entry.prefix,
                          color: Tokens.cyan,
                          tint: Tokens.cyanTint,
                        ),
                      ],
                      if (active) ...[
                        const SizedBox(width: 6),
                        const _Badge(
                            label: '当前主源 (单用)',
                            color: Tokens.ok,
                            tint: Tokens.okTint),
                      ] else if (isPartInMerged) ...[
                        const SizedBox(width: 6),
                        const _Badge(
                            label: '已启用来源',
                            color: Tokens.ok,
                            tint: Tokens.okTint),
                      ],
                    ]),
                    const SizedBox(height: 4),
                    Text(
                      subscriptionDetail(entry),
                      style: entry.hasFailed
                          ? const TextStyle(fontSize: 12, color: Tokens.warn)
                          : Tokens.hint,
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 8),
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
            ]),
            if (uinfo != null) ...[
              const SizedBox(height: 6),
              Padding(
                padding: const EdgeInsets.only(left: 44, right: 8),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    ClipRRect(
                      borderRadius: BorderRadius.circular(3),
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
                    Row(
                      mainAxisAlignment: MainAxisAlignment.spaceBetween,
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
                    const SizedBox(height: 3),
                    Text(
                      entry.quotaError.isNotEmpty
                          ? '刷新失败：${entry.quotaError} · 上次更新 ${updatedLabel(entry.quotaUpdatedAt > 0 ? entry.quotaUpdatedAt : entry.updatedAt)}'
                          : '用量更新于 ${updatedLabel(entry.quotaUpdatedAt > 0 ? entry.quotaUpdatedAt : entry.updatedAt)}',
                      style: entry.quotaError.isNotEmpty
                          ? const TextStyle(fontSize: 11, color: Tokens.warn)
                          : Tokens.hint,
                    ),
                  ],
                ),
              ),
            ],
            if (uinfo == null)
              Padding(
                padding: const EdgeInsets.only(left: 44, right: 8, bottom: 4),
                child: Text(
                  entry.quotaError.isNotEmpty
                      ? '流量刷新失败：${entry.quotaError}'
                      : '暂无流量信息，可点击“刷新剩余流量”获取',
                  style: entry.quotaError.isNotEmpty
                      ? const TextStyle(fontSize: 12, color: Tokens.warn)
                      : Tokens.hint,
                ),
              ),
          ],
        ],
      ),
    );
  }
}
