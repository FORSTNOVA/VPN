part of '../main.dart';

/// Everything that proves the connection actually works, laid out the way the IP
/// toolboxes lay it out: one pass over the facts, every row carrying its own
/// verdict, and the whole thing copyable as text.
extension _ChecksPage on _HomePageState {
  Widget _checksPage() => _PageBody(children: [
        _SectionHeader(
          title: '检测',
          trailing: Wrap(spacing: 10, runSpacing: 8, children: [
            _AkHoverButtonWrap(
              color: Tokens.cyan,
              child: FilledButton.tonalIcon(
                onPressed: _diagnosing || _port == null ? null : _runDiagnose,
                icon: const Icon(Icons.travel_explore_rounded, size: 16),
                label: Text(_diagnosing ? '检测中…' : '开始检测'),
              ),
            ),
            _AkHoverButtonWrap(
              color: Tokens.cyan,
              child: OutlinedButton.icon(
                onPressed: _diagnose == null ? null : _copyReport,
                icon: const Icon(Icons.copy_all_rounded, size: 16),
                label: const Text('复制报告'),
              ),
            ),
          ]),
        ),
        const Text(
          '一次检测依次查看：出口地址与运营商、本机 DNS 走了哪个解析器、UDP 与 TCP 是否同一出口、'
          '以及订阅要用的几个站点能否连通。未连接时会临时启动内核，但不会修改系统代理。',
          style: Tokens.small,
        ),
        const SizedBox(height: 16),
        if (_diagnose == null)
          const _Panel(
            child: _EmptyState(text: '还没有检测结果。点击「开始检测」查看当前节点的完整情况。'),
          )
        else ...[
          _diagnoseNodeRow(_diagnose!),
          const SizedBox(height: 12),
          _egressPanel(_diagnose!),
          const SizedBox(height: 18),
          _ipQualityPanel(_diagnose!),
          const SizedBox(height: 18),
          _dnsPanel(_diagnose!),
          const SizedBox(height: 18),
          _udpPanel(_diagnose!),
          const SizedBox(height: 18),
          _sitesPanel(_diagnose!),
        ],
        const SizedBox(height: 26),
        _pathCheckSection(),
        const SizedBox(height: 26),
        _speedSection(),
        const SizedBox(height: 26),
        _tunSection(),
      ]);

  Widget _diagnoseNodeRow(DiagnoseReport report) => Row(children: [
        const Text('检测节点', style: Tokens.hint),
        const SizedBox(width: 10),
        Expanded(
          child: Text(report.node.isEmpty ? '未知' : report.node,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w600)),
        ),
        if (report.temporary)
          const _Badge(
              label: '临时启动内核', color: Tokens.inkMuted, tint: Tokens.idleTint),
      ]);

  /// Where the traffic comes out and who runs the address it comes out of.
  Widget _egressPanel(DiagnoseReport report) {
    final egress = report.egress;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      const _SectionHeader(title: '出口信息'),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: '出口 IPv4',
            value: egressAddressLabel(
                address: egress.ipv4, fallback: egress.error),
            valueColor: egress.ipv4.isEmpty ? Tokens.warn : null,
            trailing: _Badge(
              label: egress.ipv4.isEmpty ? '未取得' : '可用',
              color: egress.ipv4.isEmpty ? Tokens.warn : Tokens.ok,
              tint: egress.ipv4.isEmpty ? Tokens.warnTint : Tokens.okTint,
            ),
          ),
          _KeyValueRow(
            label: '出口 IPv6',
            value: egress.ipv6.isEmpty ? '未取得（节点通常只有 IPv4 出口）' : egress.ipv6,
            valueColor: egress.ipv6.isEmpty ? Tokens.inkMuted : null,
          ),
          _KeyValueRow(label: '地区', value: egressLocationLabel(egress)),
          _KeyValueRow(label: '运营商', value: egressOperatorLabel(egress)),
        ]),
      ),
    ]);
  }

  /// IP quality, fraud risk score and leak detection panel (integrated from IPCheck.ing toolbox).
  Widget _ipQualityPanel(DiagnoseReport report) {
    final quality = report.quality;
    final hasQuality = quality.ipType.isNotEmpty;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      _SectionHeader(
        title: 'IP 质量与纯净度评估',
        trailing: hasQuality
            ? _Badge(
                label: '风险评分：${quality.fraudScore}/100 · ${quality.riskLevel}',
                color: qualityRiskColor(quality),
                tint: Tokens.idleTint,
              )
            : null,
      ),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: 'IP 类型',
            value: quality.ipType.isEmpty ? '常规出口' : quality.ipType,
            trailing: quality.isHosting
                ? const _Badge(label: '机房 IDC', color: Tokens.inkMuted, tint: Tokens.idleTint)
                : const _Badge(label: '原生住宅', color: Tokens.ok, tint: Tokens.okTint),
          ),
          _KeyValueRow(
            label: '欺诈风险分',
            value: '${quality.fraudScore} 分（越低越纯净，0~100）',
            valueColor: qualityRiskColor(quality),
            trailing: _Badge(
              label: qualityRiskBadge(quality),
              color: qualityRiskColor(quality),
              tint: Tokens.idleTint,
            ),
          ),
          _KeyValueRow(
            label: 'WebRTC 泄露检测',
            value: quality.webrtcLeak
                ? '存在泄露风险（STUN 探测到的 UDP 地址与 TCP 出口不一致）'
                : '安全（STUN/WebRTC 探测与 TCP 出口一致）',
            valueColor: quality.webrtcLeak ? Tokens.bad : Tokens.ok,
            trailing: _Badge(
              label: quality.webrtcLeak ? '泄露风险' : '安全无泄露',
              color: quality.webrtcLeak ? Tokens.bad : Tokens.ok,
              tint: quality.webrtcLeak ? Tokens.warnTint : Tokens.okTint,
            ),
          ),
          _KeyValueRow(
            label: 'DNS 泄露检测',
            value: quality.dnsLeak
                ? '存在跨国/本地泄露风险（解析器归属地与出口国家不匹配）'
                : '安全（DNS 解析器与出口同国，无跨国泄露）',
            valueColor: quality.dnsLeak ? Tokens.warn : Tokens.ok,
            trailing: _Badge(
              label: quality.dnsLeak ? '可能泄露' : '安全无泄露',
              color: quality.dnsLeak ? Tokens.warn : Tokens.ok,
              tint: quality.dnsLeak ? Tokens.warnTint : Tokens.okTint,
            ),
          ),
          if (quality.isProxy || quality.isVpn || quality.isTor || quality.isNative || quality.isHosting) ...[
            const SizedBox(height: 6),
            Row(children: [
              const Text('网络标签', style: Tokens.hint),
              const SizedBox(width: 10),
              if (quality.isNative)
                const _Badge(label: '原生IP', color: Tokens.ok, tint: Tokens.okTint),
              if (quality.isHosting)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(label: '数据中心', color: Tokens.cyan, tint: Tokens.idleTint),
                ),
              if (quality.isProxy)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(label: '已知代理', color: Tokens.warn, tint: Tokens.warnTint),
                ),
              if (quality.isVpn)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(label: '商业VPN', color: Tokens.inkMuted, tint: Tokens.idleTint),
                ),
              if (quality.isTor)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(label: 'Tor节点', color: Tokens.bad, tint: Tokens.warnTint),
                ),
            ]),
          ],
          const SizedBox(height: 10),
          const Text(
            '整合自 IPCheck.ing / MyIP 检测体系：通过多源威胁情报、ASN 属性分析及 STUN/DNS 双重对比，'
            '评估出口 IP 的住宅原生度、欺诈分与泄露防护情况。',
            style: Tokens.hint,
          ),
        ]),
      ),
    ]);
  }

  /// Which resolver answers for us, and whether it belongs where the exit is.
  Widget _dnsPanel(DiagnoseReport report) {
    final dns = report.dns;
    final known = dns.error.isEmpty && dns.resolver.isNotEmpty;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      const _SectionHeader(title: 'DNS 解析出口'),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: '解析器',
            value: known ? dns.resolver : (dns.error.isEmpty ? '未测出' : dns.error),
            valueColor: known ? null : Tokens.warn,
          ),
          _KeyValueRow(
              label: '归属', value: dns.geo.isEmpty ? '归属未知' : dns.geo),
          _KeyValueRow(
            label: '判定',
            value: dnsVerdictLabel(dns),
            valueColor: !known
                ? Tokens.inkMuted
                : (dns.matchesExit ? Tokens.ok : Tokens.warn),
            trailing: _Badge(
              label: dnsVerdictBadge(dns),
              color: !known
                  ? Tokens.inkMuted
                  : (dns.matchesExit ? Tokens.ok : Tokens.warn),
              tint: !known
                  ? Tokens.idleTint
                  : (dns.matchesExit ? Tokens.okTint : Tokens.warnTint),
            ),
          ),
          const SizedBox(height: 10),
          const Text(
            '「在出口所在地解析」说明名称是经隧道解析的；「在本地解析」说明查询由本机发出 —— '
            '系统代理模式下浏览器可能另有解析，TUN 模式下所有查询都经内核，这一行才代表全设备的情况。',
            style: Tokens.hint,
          ),
        ]),
      ),
    ]);
  }

  /// Whether the node carries datagrams, and whether they leave through the same
  /// address the tunnel presents.
  Widget _udpPanel(DiagnoseReport report) {
    final udp = report.udp;
    final relay = udp.relay;
    final relayKnown = relay != null;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      const _SectionHeader(title: 'UDP / QUIC'),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: 'UDP 中继',
            value: !relayKnown
                ? '未测出'
                : (relay.reachable
                    ? '可用，往返 ${relay.latencyMs} ms'
                    : (relay.reason.isEmpty ? '不可用' : relay.reason)),
            valueColor: !relayKnown
                ? Tokens.warn
                : (relay.reachable ? null : Tokens.warn),
            trailing: _Badge(
              label: !relayKnown ? '未测出' : udpRelayLabel(relay.reachable),
              color: !relayKnown
                  ? Tokens.inkMuted
                  : udpRelayColor(relay.reachable),
              tint: Tokens.idleTint,
            ),
          ),
          _KeyValueRow(
            label: 'UDP 出口',
            value: udp.mapped.isEmpty
                ? (udp.error.isEmpty ? '未测出' : udp.error)
                : udp.mapped,
            valueColor: udp.mapped.isEmpty ? Tokens.warn : null,
          ),
          _KeyValueRow(
            label: 'WebRTC / STUN',
            value: webrtcVerdictLabel(udp, report.quality),
            valueColor: report.quality.webrtcLeak ? Tokens.bad : Tokens.ok,
            trailing: _Badge(
              label: webrtcVerdictBadge(udp, report.quality),
              color: report.quality.webrtcLeak ? Tokens.bad : Tokens.ok,
              tint: report.quality.webrtcLeak ? Tokens.warnTint : Tokens.okTint,
            ),
          ),
          const SizedBox(height: 10),
          const Text(
            '「UDP 出口」是 STUN 服务器看到的地址，与上面的出口 IP 相同才说明数据报走的是同一条隧道；'
            '不一致意味着 UDP 可能绕过了节点。节点不转发 UDP 时，走 QUIC 的客户端会卡住 —— '
            'SmartVPN 已拒绝 UDP:443，应用会自动改用 TCP。',
            style: Tokens.hint,
          ),
        ]),
      ),
    ]);
  }

  Widget _sitesPanel(DiagnoseReport report) => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: '网站连通性',
            trailing: _Badge(
              label: report.sites.isNotEmpty &&
                      report.sites.every((site) => site.state == 'skipped')
                  ? '未检测（出口不通）'
                  : '${report.sites.where((site) => site.state == 'ok').length}/${report.sites.length} 可访问',
              color: Tokens.inkMuted,
              tint: Tokens.idleTint,
            ),
          ),
          _Panel(
            child: Column(children: [
              for (final site in report.sites) _siteRow(site),
              const SizedBox(height: 6),
              const Text(
                '检测请求显式使用本机代理，不使用浏览器登录状态。节点可达不代表浏览器已经走了隧道。',
                style: Tokens.hint,
              ),
            ]),
          ),
        ],
      );

  /// Throughput through the node, at a size the user picks because it is spent
  /// from whatever the subscription allows.
  /// The same question as the section above, asked on a timer — with the tunnel
  /// moving to another node of the region until the answer is yes.
  Widget _pathCheckSection() => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: '定时通路检测',
            trailing: Row(children: [
              Text(_pathChecks.enabled ? '已开启' : '已关闭', style: Tokens.hint),
              const SizedBox(width: 6),
              Switch(
                value: _pathChecks.enabled,
                onChanged: _pathSaving
                    ? null
                    : (value) => unawaited(_savePathChecks(PathChecks(
                          enabled: value,
                          intervalSec: _pathChecks.intervalSec,
                          targets: _pathChecks.targets,
                        ))),
              ),
            ]),
          ),
          const Text(
            '按设定的间隔只检测 Google 和 YouTube。勾选的其他网址仅在检测到应用正在访问它们时才开始检测；'
            '每个候选节点都先检测 Google 和 YouTube，两者全通后才检测其他网址。其他网址未全通时保留原地区，'
            '30 秒后继续；Google 或 YouTube 不通时会提示，并在 30 秒后自动扫描同地区节点。'
            '站点有应答就算通，因此 403、429 这类限制访问不会被当成断线。',
            style: Tokens.small,
          ),
          const SizedBox(height: 14),
          _Panel(
            child:
                Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              // The interval and the button that applies it stay together; the
              // button that runs a round now moves to its own line on a phone,
              // where the four do not fit across the screen.
              if (MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint) ...[
                Row(children: [
                  const Text('间隔', style: Tokens.hint),
                  const SizedBox(width: 10),
                  SizedBox(
                    width: 120,
                    child: TextField(
                      controller: _pathInterval,
                      enabled: !_pathSaving,
                      decoration: const InputDecoration(suffixText: '秒'),
                    ),
                  ),
                  const SizedBox(width: 8),
                  TextButton(
                    onPressed: _pathSaving
                        ? null
                        : () => unawaited(_applyPathInterval()),
                    child: const Text('应用'),
                  ),
                ]),
                const SizedBox(height: 8),
                Align(
                  alignment: Alignment.centerLeft,
                  child: OutlinedButton.icon(
                    onPressed: _busy || !_connected
                        ? null
                        : () => unawaited(_runPathChecksNow()),
                    icon: const Icon(Icons.play_arrow_rounded, size: 16),
                    label: const Text('立即检测一次'),
                  ),
                ),
              ] else
                Row(children: [
                  const Text('间隔', style: Tokens.hint),
                  const SizedBox(width: 10),
                  SizedBox(
                    width: 120,
                    child: TextField(
                      controller: _pathInterval,
                      enabled: !_pathSaving,
                      decoration: const InputDecoration(suffixText: '秒'),
                    ),
                  ),
                  const SizedBox(width: 8),
                  TextButton(
                    onPressed: _pathSaving
                        ? null
                        : () => unawaited(_applyPathInterval()),
                    child: const Text('应用'),
                  ),
                  const Spacer(),
                  OutlinedButton.icon(
                    onPressed: _busy || !_connected
                        ? null
                        : () => unawaited(_runPathChecksNow()),
                    icon: const Icon(Icons.play_arrow_rounded, size: 16),
                    label: const Text('立即检测一次'),
                  ),
                ]),
              const SizedBox(height: 10),
              for (final target in _pathChecks.targets) _pathTargetRow(target),
              const SizedBox(height: 12),
              const Divider(),
              const SizedBox(height: 10),
              // Two fields and a button do not fit across a phone: the address
              // field would be squeezed to a few characters, which is not a
              // field anyone can type an address into.
              if (MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint) ...[
                TextField(
                  controller: _pathLabel,
                  enabled: !_pathSaving,
                  decoration: const InputDecoration(labelText: '名称（可留空）'),
                ),
                const SizedBox(height: 8),
                TextField(
                  controller: _pathURL,
                  enabled: !_pathSaving,
                  decoration: const InputDecoration(
                    labelText: '自定义网址',
                    hintText: 'https://…',
                  ),
                ),
                const SizedBox(height: 8),
                Align(
                  alignment: Alignment.centerLeft,
                  child: FilledButton.tonal(
                    onPressed:
                        _pathSaving ? null : () => unawaited(_addPathTarget()),
                    child: const Text('添加'),
                  ),
                ),
              ] else
                Row(children: [
                  SizedBox(
                    width: 140,
                    child: TextField(
                      controller: _pathLabel,
                      enabled: !_pathSaving,
                      decoration:
                          const InputDecoration(labelText: '名称（可留空）'),
                    ),
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: TextField(
                      controller: _pathURL,
                      enabled: !_pathSaving,
                      decoration: const InputDecoration(
                        labelText: '自定义网址',
                        hintText: 'https://…',
                      ),
                    ),
                  ),
                  const SizedBox(width: 10),
                  FilledButton.tonal(
                    onPressed:
                        _pathSaving ? null : () => unawaited(_addPathTarget()),
                    child: const Text('添加'),
                  ),
                ]),
              const SizedBox(height: 6),
              Text(
                '间隔 ${_pathLimits.minIntervalSec}–${_pathLimits.maxIntervalSec} 秒；'
                '最多 ${_pathLimits.maxTargets} 个目标（内置项也计入）。',
                style: Tokens.hint,
              ),
            ]),
          ),
          const SizedBox(height: 14),
          _pathStatusPanel(),
        ],
      );

  /// What the monitor found last time, and what it did about it.
  Widget _pathStatusPanel() => _Panel(
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            _StatusDot(
                color: _pathStatus.failures.isEmpty ? Tokens.ok : Tokens.warn),
            const SizedBox(width: 8),
            Expanded(
              child: Text(pathSummary(_pathStatus),
                  style: const TextStyle(
                      fontSize: 13, fontWeight: FontWeight.w600)),
            ),
            if (_pathStatus.hasRun)
              Text('上次 ${clockLabel(_pathStatus.at)}', style: Tokens.hint),
          ]),
          if (_pathStatus.activeNode.isNotEmpty) ...[
            const SizedBox(height: 6),
            Text('检测节点：${_pathStatus.activeNode}', style: Tokens.hint),
          ],
          if (pathSwitchLabel(_pathStatus).isNotEmpty) ...[
            const SizedBox(height: 10),
            _Notice(
              kind: _pathStatus.failures.isEmpty
                  ? _NoticeKind.ok
                  : _NoticeKind.warn,
              text: pathSwitchLabel(_pathStatus),
            ),
          ],
          if (_pathStatus.note.isNotEmpty) ...[
            const SizedBox(height: 10),
            _Notice(kind: _NoticeKind.warn, text: _pathStatus.note),
          ],
          const SizedBox(height: 12),
          if (_pathStatus.results.isEmpty)
            Text(
              _connected
                  ? '还没有检测记录。开启后按间隔检测 Google 和 YouTube；其他网址只在检测到访问时检测。也可以点「立即检测一次」。'
                  : '连接后才能检测通路。',
              style: Tokens.hint,
            )
          else ...[
            const Divider(),
            const SizedBox(height: 6),
            for (final result in _pathStatus.results) _siteRow(result),
          ],
        ]),
      );

  Widget _pathTargetRow(PathTarget target) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 2),
        child: Row(children: [
          Checkbox(
            value: target.enabled,
            visualDensity: VisualDensity.compact,
            onChanged: _pathSaving ? null : (_) => _togglePathTarget(target),
          ),
          Expanded(
            child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(target.label,
                      style: const TextStyle(
                          fontSize: 13, fontWeight: FontWeight.w500)),
                  Text(target.url,
                      overflow: TextOverflow.ellipsis, style: Tokens.hint),
                ]),
          ),
          if (!target.builtin)
            _AkHoverIconButton(
              padding: EdgeInsets.zero,
              size: 16,
              tooltip: '删除这个网址',
              previewCode: 'REMOVE',
              color: Tokens.bad,
              onPressed: _pathSaving ? null : () => _removePathTarget(target),
              icon: const Icon(Icons.delete_outline_rounded, size: 16),
            ),
        ]),
      );

  Widget _speedSection() => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: '网速检测',
            trailing: _AkHoverButtonWrap(
              color: Tokens.cyan,
              child: FilledButton.tonalIcon(
                onPressed: _speedRunning || _port == null ? null : _runSpeedTest,
                icon: const Icon(Icons.speed_rounded, size: 16),
                label: Text(_speedRunning ? '测速中…' : '开始测速'),
              ),
            ),
          ),
          const Text(
            '经当前节点到 Cloudflare 的实测吞吐，不是线路的理论带宽。测速会真实消耗套餐流量：'
            '所选大小是下载上限，上传按它的三分之一计，两者各自最多测 10 秒。',
            style: Tokens.small,
          ),
          const SizedBox(height: 12),
          SegmentedButton<int>(
            segments: const [
              ButtonSegment(value: 10, label: Text('10 MB')),
              ButtonSegment(value: 30, label: Text('30 MB')),
              ButtonSegment(value: 100, label: Text('100 MB')),
            ],
            selected: {_speedMegaBytes},
            onSelectionChanged: _speedRunning
                ? null
                : (selection) =>
                    _refresh(() => _speedMegaBytes = selection.first),
          ),
          const SizedBox(height: 14),
          _Panel(
            child: _speed == null
                ? const _EmptyState(
                    text: '还没有测速结果。选好大小后点「开始测速」。')
                : Column(children: [
                    _KeyValueRow(
                      label: '下行',
                      value: speedLabel(_speed!.downMbps),
                      valueColor: _speed!.downMbps > 0 ? Tokens.ok : Tokens.warn,
                    ),
                    _KeyValueRow(
                      label: '上行',
                      value: speedLabel(_speed!.upMbps),
                      valueColor: _speed!.upMbps > 0 ? Tokens.ok : Tokens.warn,
                    ),
                    _KeyValueRow(label: '实测流量', value: speedDetailLabel(_speed!)),
                    _KeyValueRow(label: '测速目标', value: _speed!.target),
                    if (_speed!.error.isNotEmpty) ...[
                      const SizedBox(height: 12),
                      _Notice(kind: _NoticeKind.warn, text: _speed!.error),
                    ],
                  ]),
          ),
        ],
      );

  Widget _tunSection() => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: 'TUN 路径检查',
            trailing: _AkHoverButtonWrap(
              color: Tokens.cyan,
              child: OutlinedButton.icon(
                onPressed: _busy ? null : _runTunChecks,
                icon: const Icon(Icons.fact_check_outlined, size: 16),
                label: const Text('检查路径'),
              ),
            ),
          ),
          const Text(
            '在 TUN 模式下对比「系统路径」与「隧道出口」的地址：两者一致，才说明受保护流量确实经过内核。'
            '检查还会确认虚拟网卡、默认路由、DNS 接管与节点域名解析。',
            style: Tokens.small,
          ),
          const SizedBox(height: 16),
          if (_tunChecks.isEmpty)
            _Panel(
              child: _EmptyState(
                text: _connectionMode != 'tun'
                    ? '路径检查只在 TUN 模式连接后才有意义。当前是系统代理模式。'
                    : (_connected
                        ? '还没有检查结果。点击「检查路径」运行一次。'
                        : '路径检查只在 TUN 模式连接后才有意义。当前是 TUN 模式，但还没有连接。'),
                actionLabel: _connectionMode == 'tun' ? null : '切换到 TUN',
                onAction: _connectionMode == 'tun'
                    ? null
                    : () => _goTo(_AppPage.connect),
              ),
            )
          else
            _Panel(
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    for (final check in _tunChecks) _tunCheckRow(check),
                    const SizedBox(height: 6),
                    Text(
                      _tunChecksActive
                          ? '出口一致表示系统流量确实进入隧道；不一致说明有流量绕过内核。'
                          : '当前不是 TUN 模式或尚未连接，以上为前置条件检查。',
                      style: Tokens.hint,
                    ),
                  ]),
            ),
        ],
      );

  Widget _siteRow(SiteCheck site) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 6),
        child: Row(children: [
          Icon(
              site.state == 'ok'
                  ? Icons.check_circle_rounded
                  : Icons.info_outline_rounded,
              size: 18,
              color: siteStateColor(site.state)),
          const SizedBox(width: 10),
          SizedBox(
            width: 86,
            child: Text(site.name,
                style: const TextStyle(
                    fontSize: 13, fontWeight: FontWeight.w500)),
          ),
          SizedBox(
            width: 76,
            child: Text(siteStateLabel(site.state),
                style: TextStyle(fontSize: 12, color: siteStateColor(site.state))),
          ),
          Expanded(child: Text(site.detail, style: Tokens.hint)),
          if (site.latencyMs > 0)
            Text('${site.latencyMs} ms', style: Tokens.number),
          if (site.httpCode > 0) ...[
            const SizedBox(width: 12),
            SizedBox(
              width: 42,
              child: Text('${site.httpCode}',
                  textAlign: TextAlign.right, style: Tokens.hint),
            ),
          ],
        ]),
      );

  Widget _tunCheckRow(TunCheck check) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 7),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Icon(
              check.state == 'ok'
                  ? Icons.check_circle_rounded
                  : check.state == 'warn'
                      ? Icons.info_outline_rounded
                      : Icons.error_outline_rounded,
              size: 18,
              color: tunCheckColor(check.state)),
          const SizedBox(width: 10),
          SizedBox(
            width: 116,
            child: Text(check.name,
                style: const TextStyle(
                    fontSize: 13, fontWeight: FontWeight.w500)),
          ),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(check.detail, style: const TextStyle(fontSize: 12.5, height: 1.45)),
              if (check.value.isNotEmpty)
                Text(check.value, style: Tokens.number),
            ]),
          ),
          const SizedBox(width: 10),
          _Badge(
              label: tunCheckLabel(check.state),
              color: tunCheckColor(check.state),
              tint: Tokens.idleTint),
        ]),
      );
}
