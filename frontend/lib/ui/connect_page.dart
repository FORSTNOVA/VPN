part of '../main.dart';

/// The Rhodes Island Operations Terminal Overview (总览).
/// Answers at a glance: connection state, route flow, real-time telemetry,
/// and core proxy/routing mode controls.
extension _ConnectPage on _HomePageState {
  Future<void> _editSocksPort() async {
    final form = GlobalKey<FormState>();
    var input = _socksPort == 0 ? '' : '$_socksPort';
    final port = await showDialog<int>(
      context: context,
      builder: (dialog) {
        void save() {
          if (form.currentState!.validate()) {
            Navigator.pop(dialog, int.tryParse(input.trim()) ?? 0);
          }
        }
        return AlertDialog(
          title: const Text('SOCKS5 代理端口'),
          content: Form(
            key: form,
            child: TextFormField(
              initialValue: input,
              autofocus: true,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(
                labelText: '本地端口',
                helperText: '留空自动分配；固定端口在重连和重启后生效。',
              ),
              onChanged: (value) => input = value,
              onFieldSubmitted: (_) => save(),
              validator: (value) {
                final text = (value ?? '').trim();
                if (text.isEmpty) return null;
                final n = int.tryParse(text, radix: 10);
                return n == null || n < 1 || n > 65535 ? '请输入 1–65535' : null;
              },
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(dialog), child: const Text('取消')),
            FilledButton(onPressed: save, child: const Text('保存')),
          ],
        );
      },
    );
    if (port != null && mounted) {
      await _setConnectionMode(_connectionMode, socksPort: port);
    }
  }

  Widget _connectPage() {
    final tunMode = _connectionMode == 'tun';
    final hasNoNodes = _nodes.isEmpty && _savedSubscriptions.isEmpty;

    final route = _PathSurface(
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
      onTapNode: () => _goTo(_AppPage.nodes),
      stats: [
        (
          '出口',
          exitAddressLabel(
              ipv4: _diagnose?.egress.ipv4 ?? '',
              ipv4Error: _diagnose?.egress.error ?? '')
        ),
        ('延迟', _latency == null ? '—' : '$_latency ms'),
        ('锁定地区', _lockedRegion.isEmpty ? '未锁定' : _lockedRegion),
        ('候选节点', '$_poolSize'),
      ],
    );
    final command = _PowerSwitchHero(
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
    );
    return _PageBody(children: [
      LayoutBuilder(builder: (context, constraints) {
        if (constraints.maxWidth < 760) {
          return Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [command, const SizedBox(height: 16), route]);
        }
        return Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Expanded(flex: 5, child: command),
          const SizedBox(width: 16),
          Expanded(flex: 6, child: route),
        ]);
      }),

      // 3. Compact Real Telemetry Row (Shown when kernel is active or telemetry available)
      if (_connected && _traffic.available) ...[
        const SizedBox(height: 14),
        _Panel(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
          child: Row(
            children: [
              Expanded(
                child: _Stats(items: [
                  ('上行速率', rateLabel(_traffic.uploadRate)),
                  ('下行速率', rateLabel(_traffic.downloadRate)),
                  ('本次上行', byteLabel(_traffic.uploadTotal)),
                  ('本次下行', byteLabel(_traffic.downloadTotal)),
                  ('当前连接', '${_traffic.connectionCount}'),
                ]),
              ),
              IconButton(
                tooltip: '查看连接明细',
                icon: const Icon(Icons.arrow_forward_rounded, size: 16),
                onPressed: () => _goTo(_AppPage.traffic),
              ),
            ],
          ),
        ),
      ],

      // 4. Actionable First-run Empty State (When no nodes or subscriptions are configured)
      if (hasNoNodes) ...[
        const SizedBox(height: 14),
        _Panel(
          padding: const EdgeInsets.all(20),
          child: _EmptyState(
            illustration: ClipRRect(
              borderRadius: BorderRadius.circular(Tokens.radiusBadge),
              child: Image.asset(
                'assets/amiya_chibi.png',
                height: 52,
                fit: BoxFit.contain,
                errorBuilder: (_, __, ___) => const Icon(
                  Icons.hub_outlined,
                  size: 48,
                  color: Tokens.yellow,
                ),
              ),
            ),
            text: '终端就绪 · 尚未配置节点或订阅。\n导入订阅链接或添加自定义节点后即可开始守护网络。',
            actionLabel: '导入订阅链接',
            onAction: () => _goTo(_AppPage.subscription),
            secondaryActionLabel: '添加自定义节点',
            onSecondaryAction: () => unawaited(_showAddNodeDialog()),
          ),
        ),
      ],

      // 5. System Notices (Blocked, Proxy tampered, Sweep note)
      if (_blocked) ...[
        const SizedBox(height: 14),
        _Notice(
          kind: _NoticeKind.bad,
          text: '受保护流量已阻断：${_blockReason.isEmpty ? '同地区没有可用节点' : _blockReason}。'
              'SmartVPN 正在等待同地区节点恢复，不会跨地区直连。',
          action: TextButton(
            onPressed: () => _goTo(_AppPage.region),
            child: const Text('查看保护策略'),
          ),
        ),
      ] else if (_health?.sweepNote.isNotEmpty ?? false) ...[
        const SizedBox(height: 14),
        _Notice(
          kind: _NoticeKind.warn,
          text: _health!.sweepNote,
          action: TextButton(
            onPressed: () => _goTo(_AppPage.region),
            child: const Text('查看保护策略'),
          ),
        ),
      ],
      if (_connectionMode == 'system-proxy' &&
          _connected &&
          !_proxyEnabled) ...[
        const SizedBox(height: 14),
        _Notice(
          kind: _NoticeKind.warn,
          text: 'Windows 系统代理已关闭或被其他程序修改，浏览器可能未经过 SmartVPN。',
          action: TextButton(
            onPressed: _busy ? null : _reapplyProxy,
            child: const Text('重新启用'),
          ),
        ),
      ],

      const SizedBox(height: 22),

      // 6. Purposeful Core Configuration Sections
      _Panel(
        padding: EdgeInsets.zero,
        child: ExpansionTile(
            title: const Text('代理模式与分流'),
            subtitle: Text(Platform.isAndroid
                ? 'VPN 全设备'
                : (tunMode ? 'TUN 全设备' : '系统代理')),
            childrenPadding: const EdgeInsets.fromLTRB(18, 0, 18, 18),
            expandedCrossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (Platform.isAndroid)
                const Text(
                  '连接由系统的 VPN 服务承载：授权之后，全部应用的流量都会经过 SmartVPN。',
                  style: Tokens.small,
                )
              else ...[
                if (Platform.isWindows)
                  ListTile(
                    contentPadding: EdgeInsets.zero,
                    title: const Text('SOCKS5 代理端口'),
                    subtitle: Text(_connected
                        ? '127.0.0.1:$_mixedPort · 断开后可修改'
                        : (_socksPort == 0 ? '自动分配' : '127.0.0.1:$_socksPort · 固定')),
                    trailing: const Icon(Icons.edit_outlined),
                    onTap: _busy || _connected ? null : _editSocksPort,
                  ),
                SizedBox(
                  width: 320,
                  child: SegmentedButton<String>(
                    segments: const [
                      ButtonSegment(
                        value: 'system-proxy',
                        label: Text('系统代理'),
                        icon: Icon(Icons.language_rounded, size: 16),
                      ),
                      ButtonSegment(
                        value: 'tun',
                        label: Text('TUN 全设备'),
                        icon: Icon(Icons.vpn_lock_rounded, size: 16),
                      ),
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
                      ? 'TUN 由内核创建虚拟网卡接管系统默认路由，不遵循系统代理的命令行及后台程序也会进入隧道。'
                      : '浏览器的 WebRTC 和 DNS 可能绕过系统代理。需要保护时，断开连接，选择「TUN 全设备」并开启防泄露。',
                  style: Tokens.small,
                ),
              ],
              const SizedBox(height: 14),
              const Divider(),
              const SizedBox(height: 12),
              if (tunMode || Platform.isAndroid) ...[
                SwitchListTile.adaptive(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('国内加速'),
                  subtitle: const Text(
                      '国内服务的 TCP 连接直连，包括网页和游戏端口。开启时同时启用两项保护，可分别调整。',
                      style: Tokens.small),
                  value: _domesticWeb,
                  onChanged: _busy || _connected
                      ? null
                      : (value) => unawaited(_setConnectionMode(_connectionMode,
                          domesticWeb: value)),
                ),
                SwitchListTile.adaptive(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('WebRTC 防泄露'),
                  subtitle: const Text('公网 UDP 经节点中继；节点不支持时阻断，通话可能受影响。',
                      style: Tokens.small),
                  value: _webRTCProtection,
                  onChanged: _busy || _connected
                      ? null
                      : (value) => unawaited(_setConnectionMode(_connectionMode,
                          webRTCProtection: value)),
                ),
                SwitchListTile.adaptive(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('DNS 防泄露'),
                  subtitle: const Text('国内名单走国内加密 DNS；国外及未识别域名经节点加密解析。',
                      style: Tokens.small),
                  value: _dnsProtection,
                  onChanged: _busy || _connected
                      ? null
                      : (value) => unawaited(_setConnectionMode(_connectionMode,
                          dnsProtection: value)),
                ),
                ListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('游戏直连名单'),
                  subtitle: Text(_gameBypass.isEmpty
                      ? '未添加游戏'
                      : '已添加 ${_gameBypass.length} 项'),
                  trailing: const Icon(Icons.edit_outlined),
                  enabled: !_busy && !_connected,
                  onTap: () async {
                    final names = await editGameBypass(
                        context, _gameBypass, Platform.isAndroid);
                    if (names != null && mounted) {
                      await _setConnectionMode(_connectionMode,
                          gameBypass: names);
                    }
                  },
                ),
              ],
              if (_domesticWeb ||
                  !(_webRTCProtection || _dnsProtection) ||
                  !(tunMode || Platform.isAndroid))
                _chinaDirectRow(),
              if (!Platform.isAndroid && tunMode) ...[
                const SizedBox(height: 14),
                _tunReadiness(),
              ],
            ]),
      ),
      if (Platform.isAndroid)
        Align(
          alignment: Alignment.centerLeft,
          child: TextButton.icon(
            style: TextButton.styleFrom(
              visualDensity: VisualDensity.compact,
              padding: EdgeInsets.zero,
              tapTargetSize: MaterialTapTargetSize.shrinkWrap,
            ),
            onPressed: _openGitHubRelease,
            icon: const Icon(Icons.open_in_new, size: 16),
            label: const Text('到 GitHub 查看更新'),
          ),
        ),
    ]);
  }

  Future<void> _openGitHubRelease() async {
    try {
      await _HomePageState._host.invokeMethod<void>('openGitHubRelease');
    } on PlatformException catch (error) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(error.message ?? '无法打开 GitHub 发布页')),
      );
    }
  }

  /// The country's own addresses going direct is what keeps a domestic site off
  /// the node. Without the list, every address a subscription does not name is
  /// carried abroad.
  Widget _chinaDirectRow() {
    final china = _chinaDirect;
    final settled = china.available && china.active;
    return Row(children: [
      Icon(
        settled
            ? Icons.check_circle_outline_rounded
            : Icons.info_outline_rounded,
        size: 16,
        color: settled ? Tokens.ok : Tokens.warn,
      ),
      const SizedBox(width: 10),
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
      OutlinedButton(
        onPressed: _busy || china.refreshing
            ? null
            : () => unawaited(_refreshChinaDirect()),
        child: Text(china.refreshing
            ? '获取中…'
            : (china.available ? '更新直连列表' : '获取直连列表')),
      ),
    ]);
  }

  /// TUN needs an administrator token and wintun.dll.
  Widget _tunReadiness() {
    if (_tunAvailable) {
      return const _Notice(
        kind: _NoticeKind.ok,
        text: '管理员权限与 wintun.dll 均已就绪，TUN 模式可以正常连接。',
      );
    }
    final needsElevation = !_elevated;
    final needsWintun = !(_tun?.wintun.installed ?? false);
    return _Notice(
      kind: _NoticeKind.warn,
      text: 'TUN 尚不可用：${_tunReason.isEmpty ? '前置条件未满足' : _tunReason}。',
      action: Wrap(spacing: 8, runSpacing: 6, children: [
        if (needsWintun)
          FilledButton.tonal(
            onPressed: _busy ? null : _downloadWintun,
            child: const Text('下载 wintun.dll'),
          ),
        if (needsElevation)
          FilledButton.tonal(
            onPressed: _busy ? null : _relaunchElevated,
            child: const Text('以管理员身份重启'),
          ),
        TextButton(
          onPressed: () => _goTo(_AppPage.subscription),
          child: const Text('内核高级设置'),
        ),
      ]),
    );
  }
}
