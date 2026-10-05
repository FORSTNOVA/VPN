part of '../main.dart';

/// The landing page. Its job is to answer one question at a glance: where is my
/// traffic going right now, and is anything protecting it.
extension _ConnectPage on _HomePageState {
  Widget _connectPage() {
    final tunMode = _connectionMode == 'tun';
    return _PageBody(children: [
      _PathSurface(
        stateLabel: protectionStateLabel(
          connected: _connected,
          blocked: _blocked,
          mode: _connectionMode,
          tunActive: _tunActive,
          proxyEnabled: _proxyEnabled,
        ),
        nodeLabel: _connected
            ? (_activeNode.isEmpty ? '正在选择节点…' : _activeNode)
            : (_selectedNode ?? '自动选择'),
        regionLabel: _activeRegion.isNotEmpty
            ? _activeRegion
            : (_lockedRegion.isNotEmpty ? '锁定 $_lockedRegion' : '未锁定'),
        connected: _protectedNow,
        stats: [
          ('出口',
              exitAddressLabel(
                  ipv4: _diagnose?.egress.ipv4 ?? '',
                  ipv4Error: _diagnose?.egress.error ?? '')),
          ('延迟', _latency == null ? '—' : '$_latency ms'),
          ('锁定地区', _lockedRegion.isEmpty ? '未锁定' : _lockedRegion),
          ('候选节点', '$_poolSize'),
        ],
      ),
      const SizedBox(height: 14),
      _PowerSwitchHero(
        connected: _connected,
        busy: _busy,
        port: _port,
        latency: _latency,
        onToggle: _toggleConnection,
        onMeasure: _measureLatency,
        statusDetail: protectionStateDetail(
          connected: _connected,
          blocked: _blocked,
          mode: _connectionMode,
          tunActive: _tunActive,
          proxyEnabled: _proxyEnabled,
        ),
      ),
      if (_blocked) ...[
        const SizedBox(height: 14),
        _Notice(
          kind: _NoticeKind.bad,
          text: '受保护流量已阻断：${_blockReason.isEmpty ? '同地区没有可用节点' : _blockReason}。'
              'SmartVPN 正在等待同地区节点恢复，不会跨地区直连。',
          action: TextButton(
            onPressed: () => _goTo(_AppPage.region),
            child: const Text('查看地区'),
          ),
        ),
      ] else if (_health?.sweepNote.isNotEmpty ?? false) ...[
        // The region measured as dead while traffic still flowed, which is a
        // measurement problem rather than a dead region. Saying so is what keeps
        // the health readout from looking like a contradiction.
        const SizedBox(height: 14),
        _Notice(
          kind: _NoticeKind.warn,
          text: _health!.sweepNote,
          action: TextButton(
            onPressed: () => _goTo(_AppPage.region),
            child: const Text('查看地区'),
          ),
        ),
      ],
      if (_connectionMode == 'system-proxy' &&
          _connected &&
          !_proxyEnabled) ...[
        const SizedBox(height: 14),
        _Notice(
          kind: _NoticeKind.warn,
          text: 'Windows 系统代理已关闭或被其他程序修改，浏览器可能没有经过 SmartVPN。',
          action: TextButton(
            onPressed: _busy ? null : _reapplyProxy,
            child: const Text('重新启用'),
          ),
        ),
      ],
      const SizedBox(height: 26),
      const _SectionHeader(title: '代理模式'),
      _Panel(
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          if (Platform.isAndroid)
            // One way to carry traffic, and the shape of it is the platform's
            // decision rather than a setting: a VpnService the system runs and
            // the user authorises, through which everything goes. Offering a
            // choice between modes here would be offering one that does not
            // exist.
            const Text('连接由系统的 VPN 服务承载：授权之后，全部应用的流量都会经过 SmartVPN。',
                style: Tokens.small)
          else ...[
            SizedBox(
              width: 320,
              child: SegmentedButton<String>(
                segments: const [
                  ButtonSegment(
                      value: 'system-proxy',
                      label: Text('系统代理'),
                      icon: Icon(Icons.language_rounded, size: 16)),
                  ButtonSegment(
                      value: 'tun',
                      label: Text('TUN 全设备'),
                      icon: Icon(Icons.vpn_lock_rounded, size: 16)),
                ],
                selected: {_connectionMode},
                onSelectionChanged: _busy || _connected
                    ? null
                    : (selection) =>
                        unawaited(_setConnectionMode(selection.first)),
              ),
            ),
            const SizedBox(height: 12),
            Text(
              tunMode
                  ? 'TUN 由内核创建虚拟网卡接管默认路由，不使用系统代理的程序也会进入隧道。'
                  : '系统代理只覆盖遵循 Windows 代理设置的程序，其余程序不受影响。',
              style: Tokens.small,
            ),
          ],
          const SizedBox(height: 14),
          const Divider(),
          const SizedBox(height: 10),
          _chinaDirectRow(),
          if (!Platform.isAndroid && tunMode) ...[
            const SizedBox(height: 14),
            _tunReadiness(),
          ],
        ]),
      ),
    ]);
  }

  /// The country's own addresses going direct is what keeps a domestic site off
  /// the node. Without the list, every address a subscription does not name is
  /// carried abroad, which is what a page that will not load looks like.
  Widget _chinaDirectRow() {
    final china = _chinaDirect;
    final settled = china.available && china.active;
    return Row(children: [
      Icon(
        settled ? Icons.check_circle_outline_rounded : Icons.info_outline_rounded,
        size: 16,
        color: settled ? Tokens.ok : Tokens.warn,
      ),
      const SizedBox(width: 8),
      Expanded(
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(chinaDirectLabel(china), style: Tokens.small),
          if (china.available && china.updatedAt.isNotEmpty)
            Text(
              '更新于 ${dayLabel(china.updatedAt)}'
              '${china.source.isEmpty ? '' : '　来源 ${china.source}'}',
              style: Tokens.hint,
            ),
        ]),
      ),
      TextButton(
        onPressed: _busy || china.refreshing
            ? null
            : () => unawaited(_refreshChinaDirect()),
        child: Text(china.refreshing
            ? '获取中…'
            : (china.available ? '更新列表' : '获取列表')),
      ),
    ]);
  }

  /// TUN needs an administrator token and wintun.dll; say exactly which one is
  /// missing and put the fix next to it.
  Widget _tunReadiness() {
    if (_tunAvailable) {
      return const _Notice(
        kind: _NoticeKind.ok,
        text: '管理员权限与 wintun.dll 均已就绪，可以连接。',
      );
    }
    final needsElevation = !_elevated;
    final needsWintun = !(_tun?.wintun.installed ?? false);
    return _Notice(
      kind: _NoticeKind.warn,
      text: 'TUN 尚不可用：${_tunReason.isEmpty ? '前置条件未满足' : _tunReason}。',
      action: Wrap(spacing: 4, children: [
        if (needsWintun)
          TextButton(
            onPressed: _busy ? null : _downloadWintun,
            child: const Text('下载 wintun.dll'),
          ),
        if (needsElevation)
          TextButton(
            onPressed: _busy ? null : _relaunchElevated,
            child: const Text('以管理员身份重启'),
          ),
        TextButton(
          onPressed: () => _goTo(_AppPage.subscription),
          child: const Text('内核设置'),
        ),
      ]),
    );
  }
}
