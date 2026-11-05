part of '../main.dart';

/// The Rhodes Island Diagnostic & Tools Suite (工具 / 检测).
/// Tests and proves connection health: exit IP, operator, DNS resolution path,
/// UDP STUN parity, target site reachability, path monitors, and TUN path checks.
extension _ChecksPage on _HomePageState {
  Widget _checksPage() => _PageBody(children: [
        _SectionHeader(
          title: '全面连接检测',
          subtitle: 'DIAGNOSTIC_SUITE',
          trailing: Wrap(
            spacing: 8,
            runSpacing: 6,
            children: [
              FilledButton.icon(
                onPressed: _diagnosing || _port == null ? null : _runDiagnose,
                icon: const Icon(Icons.travel_explore_rounded, size: 16),
                label: Text(_diagnosing ? '检测中…' : '开始检测'),
              ),
              OutlinedButton.icon(
                onPressed: _diagnose == null ? null : _copyReport,
                icon: const Icon(Icons.copy_all_rounded, size: 16),
                label: const Text('复制报告'),
              ),
            ],
          ),
        ),
        const Text(
          '单次检测完整检验：出口 IPv4/IPv6、归属地区与运营商、本机 DNS 真实解析器、'
          'UDP 与 TCP 出口一致性，以及关键目标服务连通性。未连接时会临时启动内核进行探测。',
          style: Tokens.small,
        ),
        const SizedBox(height: 14),
        if (_diagnose == null)
          const _Panel(
            child: _EmptyState(
              text: '还没有检测结果。点击「开始检测」运行当前节点的完整网络诊断。',
            ),
          )
        else ...[
          _diagnoseNodeRow(_diagnose!),
          const SizedBox(height: 12),
          _egressPanel(_diagnose!),
          const SizedBox(height: 16),
          _ipQualityPanel(_diagnose!),
          const SizedBox(height: 16),
          _dnsPanel(_diagnose!),
          const SizedBox(height: 16),
          _udpPanel(_diagnose!),
          const SizedBox(height: 16),
          _sitesPanel(_diagnose!),
        ],
        const SizedBox(height: 24),
        _pathCheckSection(),
        const SizedBox(height: 24),
        _speedSection(),
        const SizedBox(height: 24),
        _tunSection(),
      ]);

  Widget _diagnoseNodeRow(DiagnoseReport report) => Row(children: [
        const Text('当前检测节点', style: Tokens.hint),
        const SizedBox(width: 10),
        Expanded(
          child: Text(
            report.node.isEmpty ? '未知' : report.node,
            overflow: TextOverflow.ellipsis,
            style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w700),
          ),
        ),
        if (report.temporary)
          const _Badge(
            label: '临时测试内核',
            color: Tokens.inkMuted,
            tint: Tokens.idleTint,
          ),
      ]);

  /// Where the traffic exits and who operates the IP address.
  Widget _egressPanel(DiagnoseReport report) {
    final egress = report.egress;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      const _SectionHeader(
        title: '出口信息',
        subtitle: 'EGRESS_IP',
      ),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: '出口 IPv4',
            value: egressAddressLabel(
                address: egress.ipv4, fallback: egress.error),
            valueColor: egress.ipv4.isEmpty ? Tokens.warn : Tokens.cyan,
            trailing: _Badge(
              label: egress.ipv4.isEmpty ? '未取得' : '可用',
              color: egress.ipv4.isEmpty ? Tokens.warn : Tokens.ok,
              tint: egress.ipv4.isEmpty ? Tokens.warnTint : Tokens.okTint,
              showDot: true,
            ),
          ),
          _KeyValueRow(
            label: '出口 IPv6',
            value: egress.ipv6.isEmpty ? '未取得（节点通常只有 IPv4 出口）' : egress.ipv6,
            valueColor: egress.ipv6.isEmpty ? Tokens.inkMuted : Tokens.cyan,
          ),
          _KeyValueRow(label: '实测地区', value: egressLocationLabel(egress)),
          _KeyValueRow(label: '运营商 / ASN', value: egressOperatorLabel(egress)),
        ]),
      ),
    ]);
  }

  /// IP quality, fraud risk score and leak detection panel.
  Widget _ipQualityPanel(DiagnoseReport report) {
    final quality = report.quality;
    final hasQuality = quality.ipType.isNotEmpty;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      _SectionHeader(
        title: 'IP 质量与纯净度评估',
        subtitle: 'RISK_ANALYSIS',
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
                ? const _Badge(
                    label: '机房 IDC',
                    color: Tokens.inkMuted,
                    tint: Tokens.idleTint)
                : const _Badge(
                    label: '原生住宅', color: Tokens.ok, tint: Tokens.okTint),
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
              showDot: true,
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
              showDot: true,
            ),
          ),
          if (quality.isProxy ||
              quality.isVpn ||
              quality.isTor ||
              quality.isNative ||
              quality.isHosting) ...[
            const SizedBox(height: 6),
            Row(children: [
              const Text('网络特征标签', style: Tokens.hint),
              const SizedBox(width: 10),
              if (quality.isNative)
                const _Badge(
                    label: '原生IP', color: Tokens.ok, tint: Tokens.okTint),
              if (quality.isHosting)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(
                      label: '数据中心', color: Tokens.cyan, tint: Tokens.idleTint),
                ),
              if (quality.isProxy)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(
                      label: '已知代理', color: Tokens.warn, tint: Tokens.warnTint),
                ),
              if (quality.isVpn)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(
                      label: '商业VPN',
                      color: Tokens.inkMuted,
                      tint: Tokens.idleTint),
                ),
              if (quality.isTor)
                const Padding(
                  padding: EdgeInsets.only(left: 6),
                  child: _Badge(
                      label: 'Tor节点', color: Tokens.bad, tint: Tokens.warnTint),
                ),
            ]),
          ],
          const SizedBox(height: 8),
          const Text(
            '整合自 IPCheck.ing / MyIP 架构：多源对比出口 ASN、住宅属性与 STUN/DNS 一致性，防范隐私泄露。',
            style: Tokens.hint,
          ),
        ]),
      ),
    ]);
  }

  /// DNS resolution path.
  Widget _dnsPanel(DiagnoseReport report) {
    final dns = report.dns;
    final known = dns.error.isEmpty && dns.resolver.isNotEmpty;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      const _SectionHeader(
        title: 'DNS 解析出口',
        subtitle: 'DNS_RESOLVER',
      ),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: '解析器',
            value:
                known ? dns.resolver : (dns.error.isEmpty ? '未测出' : dns.error),
            valueColor: known ? Tokens.cyan : Tokens.warn,
          ),
          _KeyValueRow(
            label: '归属地',
            value: dns.geo.isEmpty ? '归属未知' : dns.geo,
          ),
          _KeyValueRow(
            label: '判定结论',
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
              showDot: true,
            ),
          ),
          const SizedBox(height: 8),
          const Text(
            '在出口所在地解析说明域名通过隧道远端代理解析；若显示在本地解析，则解析未经隧道。',
            style: Tokens.hint,
          ),
        ]),
      ),
    ]);
  }

  /// UDP / STUN datagram check.
  Widget _udpPanel(DiagnoseReport report) {
    final udp = report.udp;
    final relay = udp.relay;
    final relayKnown = relay != null;
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      const _SectionHeader(
        title: 'UDP / QUIC',
        subtitle: 'DATAGRAM_RELAY',
      ),
      _Panel(
        child: Column(children: [
          _KeyValueRow(
            label: 'UDP 中继',
            value: !relayKnown
                ? '未测出'
                : (relay.reachable
                    ? '可用，往返延迟 ${relay.latencyMs} ms'
                    : (relay.reason.isEmpty ? '不可用' : relay.reason)),
            valueColor: !relayKnown
                ? Tokens.warn
                : (relay.reachable ? Tokens.ok : Tokens.warn),
            trailing: _Badge(
              label: !relayKnown ? '未测出' : udpRelayLabel(relay.reachable),
              color: !relayKnown
                  ? Tokens.inkMuted
                  : udpRelayColor(relay.reachable),
              tint: Tokens.idleTint,
              showDot: true,
            ),
          ),
          _KeyValueRow(
            label: 'UDP 出口',
            value: udp.mapped.isEmpty
                ? (udp.error.isEmpty ? '未测出' : udp.error)
                : udp.mapped,
            valueColor: udp.mapped.isEmpty ? Tokens.warn : Tokens.cyan,
          ),
          _KeyValueRow(
            label: 'WebRTC / STUN',
            value: webrtcVerdictLabel(udp, report.quality),
            valueColor: report.quality.webrtcLeak ? Tokens.bad : Tokens.ok,
            trailing: _Badge(
              label: webrtcVerdictBadge(udp, report.quality),
              color: report.quality.webrtcLeak ? Tokens.bad : Tokens.ok,
              tint: report.quality.webrtcLeak ? Tokens.warnTint : Tokens.okTint,
              showDot: true,
            ),
          ),
          const SizedBox(height: 8),
          const Text(
            'UDP 出口为 STUN 看到的地址，与 TCP 出口一致说明数据包均在同一隧道内。'
            'SmartVPN 会自动规避 UDP:443 阻塞并平滑降级至 TCP。',
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
            subtitle: 'TARGET_PROBES',
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
                '检测请求显式经由内核端口发起。绿色表示站点连通正常。',
                style: Tokens.hint,
              ),
            ]),
          ),
        ],
      );

  /// Target path monitor section.
  Widget _pathCheckSection() => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: '定时通路检测',
            subtitle: 'PATH_MONITOR',
            trailing: Row(children: [
              Text(_pathChecks.enabled ? '已开启' : '已关闭', style: Tokens.hint),
              const SizedBox(width: 8),
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
            '按设定的间隔监控 Google 和 YouTube。勾选的其他目标在检测到应用访问时加入巡检；'
            'Google 或 YouTube 异常时将自动在同地区健康节点间容灾切换。',
            style: Tokens.small,
          ),
          const SizedBox(height: 12),
          _Panel(
            child:
                Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Wrap(
                  spacing: 8,
                  runSpacing: 10,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    const Text('巡检间隔', style: Tokens.hint),
                    const SizedBox(width: 10),
                    SizedBox(
                      width: 110,
                      child: TextField(
                        controller: _pathInterval,
                        enabled: !_pathSaving,
                        decoration: const InputDecoration(suffixText: '秒'),
                      ),
                    ),
                    const SizedBox(width: 8),
                    OutlinedButton(
                      onPressed: _pathSaving
                          ? null
                          : () => unawaited(_applyPathInterval()),
                      child: const Text('应用'),
                    ),
                    OutlinedButton.icon(
                      onPressed: _busy || !_connected
                          ? null
                          : () => unawaited(_runPathChecksNow()),
                      icon: const Icon(Icons.play_arrow_rounded, size: 16),
                      label: const Text('立即检测一次'),
                    ),
                  ]),
              const SizedBox(height: 12),
              for (final target in _pathChecks.targets) _pathTargetRow(target),
              const SizedBox(height: 12),
              const Divider(),
              const SizedBox(height: 10),
              Row(children: [
                SizedBox(
                  width: 140,
                  child: TextField(
                    controller: _pathLabel,
                    enabled: !_pathSaving,
                    decoration: const InputDecoration(labelText: '名称（可选）'),
                  ),
                ),
                const SizedBox(width: 10),
                Expanded(
                  child: TextField(
                    controller: _pathURL,
                    enabled: !_pathSaving,
                    decoration: const InputDecoration(
                      labelText: '自定义检测网址',
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
                '间隔允许范围 ${_pathLimits.minIntervalSec}–${_pathLimits.maxIntervalSec} 秒；'
                '最多支持 ${_pathLimits.maxTargets} 个监控目标。',
                style: Tokens.hint,
              ),
            ]),
          ),
          const SizedBox(height: 14),
          _pathStatusPanel(),
        ],
      );

  Widget _pathStatusPanel() => _Panel(
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            const Text('上轮检测状态',
                style: TextStyle(fontSize: 13, fontWeight: FontWeight.w700)),
            const Spacer(),
            if (_pathStatus.running)
              const _Badge(label: '巡检中', color: Tokens.ok, tint: Tokens.okTint)
            else
              const _Badge(
                  label: '空闲待命', color: Tokens.inkMuted, tint: Tokens.idleTint),
          ]),
          const SizedBox(height: 10),
          if (!_pathStatus.hasRun)
            const Text('还没有检测记录。开启后会按间隔自动运行。', style: Tokens.hint)
          else ...[
            Text('上次检测时间：${dayLabel(_pathStatus.at)}', style: Tokens.small),
            if (_pathStatus.activeNode.isNotEmpty)
              Text('检测节点：${_pathStatus.activeNode}', style: Tokens.small),
            if (_pathStatus.failures.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text('异常目标：${_pathStatus.failures.join(', ')}',
                    style: const TextStyle(fontSize: 12.5, color: Tokens.bad)),
              ),
            if (_pathStatus.switched.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text('容灾切换：${_pathStatus.switched.join(' → ')}',
                    style: const TextStyle(fontSize: 12.5, color: Tokens.warn)),
              ),
            if (_pathStatus.retryAt.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                    '下次重试：${dayLabel(_pathStatus.retryAt)}${_pathStatus.retryMode.isNotEmpty ? ' (${_pathStatus.retryMode})' : ''}',
                    style: Tokens.hint),
              ),
            if (_pathStatus.note.isNotEmpty) ...[
              const SizedBox(height: 4),
              Text(_pathStatus.note,
                  style: TextStyle(
                      fontSize: 12.5,
                      color: _pathStatus.alert.isNotEmpty
                          ? Tokens.warn
                          : Tokens.ok)),
            ],
            if (_pathStatus.alert.isNotEmpty) ...[
              const SizedBox(height: 6),
              _Notice(kind: _NoticeKind.warn, text: _pathStatus.alert),
            ],
          ],
        ]),
      );

  Widget _pathTargetRow(PathTarget target) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 4),
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
                        fontSize: 13, fontWeight: FontWeight.w600)),
                Text(target.url,
                    overflow: TextOverflow.ellipsis, style: Tokens.hint),
              ],
            ),
          ),
          if (!target.builtin)
            _AkHoverIconButton(
              padding: EdgeInsets.zero,
              size: 16,
              tooltip: '删除此网址',
              color: Tokens.bad,
              onPressed: _pathSaving ? null : () => _removePathTarget(target),
              icon: const Icon(Icons.delete_outline_rounded, size: 16),
            ),
        ]),
      );

  /// Throughput speed test section.
  Widget _speedSection() => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: '网速检测',
            subtitle: 'THROUGHPUT_TEST',
            trailing: FilledButton.tonalIcon(
              onPressed: _speedRunning || _port == null ? null : _runSpeedTest,
              icon: const Icon(Icons.speed_rounded, size: 16),
              label: Text(_speedRunning ? '测速中…' : '开始测速'),
            ),
          ),
          const Text(
            '通过当前选定节点连接 Cloudflare 节点进行多路吞吐测量。'
            '测速将消耗真实流量：选择下载额度上限，上传按其三分之一并发测试，单向测试最长 10 秒。',
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
          const SizedBox(height: 12),
          _Panel(
            child: _speed == null
                ? const _EmptyState(text: '还没有测速结果。选好大小后点「开始测速」。')
                : Column(children: [
                    _KeyValueRow(
                      label: '下行带宽',
                      value: speedLabel(_speed!.downMbps),
                      valueColor:
                          _speed!.downMbps > 0 ? Tokens.ok : Tokens.warn,
                    ),
                    _KeyValueRow(
                      label: '上行带宽',
                      value: speedLabel(_speed!.upMbps),
                      valueColor: _speed!.upMbps > 0 ? Tokens.ok : Tokens.warn,
                    ),
                    _KeyValueRow(
                      label: '消耗流量',
                      value: speedDetailLabel(_speed!),
                    ),
                    _KeyValueRow(label: '测试目标', value: _speed!.target),
                    if (_speed!.error.isNotEmpty) ...[
                      const SizedBox(height: 10),
                      _Notice(kind: _NoticeKind.warn, text: _speed!.error),
                    ],
                  ]),
          ),
        ],
      );

  /// TUN route inspection section.
  Widget _tunSection() => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _SectionHeader(
            title: 'TUN 路径检查',
            subtitle: 'ROUTING_INTEGRITY',
            trailing: OutlinedButton.icon(
              onPressed: _busy ? null : _runTunChecks,
              icon: const Icon(Icons.fact_check_outlined, size: 16),
              label: const Text('检查路径'),
            ),
          ),
          const Text(
            '在 TUN 模式下校验「系统直连路径」与「内核出口路径」：两者一致才证明整机流量确实经过内核。'
            '同时确认虚拟网卡适配器、默认路由、DNS 劫持及节点直连排除配置。',
            style: Tokens.small,
          ),
          const SizedBox(height: 12),
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
                        ? '出口一致表明系统所有应用流量确实进入隧道；不一致说明有流量绕过内核。'
                        : '当前不是 TUN 模式或尚未连接，以上显示为前置环境状态。',
                    style: Tokens.hint,
                  ),
                ],
              ),
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
            color: siteStateColor(site.state),
          ),
          const SizedBox(width: 10),
          SizedBox(
            width: 90,
            child: Text(
              site.name,
              style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w600),
            ),
          ),
          SizedBox(
            width: 80,
            child: Text(
              siteStateLabel(site.state),
              style: TextStyle(fontSize: 12, color: siteStateColor(site.state)),
            ),
          ),
          Expanded(child: Text(site.detail, style: Tokens.hint)),
          if (site.latencyMs > 0)
            Text('${site.latencyMs} ms',
                style: Tokens.number.copyWith(color: Tokens.cyan)),
          if (site.httpCode > 0) ...[
            const SizedBox(width: 12),
            SizedBox(
              width: 44,
              child: Text(
                '${site.httpCode}',
                textAlign: TextAlign.right,
                style: Tokens.hint,
              ),
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
            color: tunCheckColor(check.state),
          ),
          const SizedBox(width: 10),
          SizedBox(
            width: 120,
            child: Text(
              check.name,
              style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w600),
            ),
          ),
          Expanded(
            child:
                Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(check.detail,
                  style: const TextStyle(fontSize: 12.5, height: 1.4)),
              if (check.value.isNotEmpty)
                Text(check.value, style: Tokens.number),
            ]),
          ),
          const SizedBox(width: 10),
          _Badge(
            label: tunCheckLabel(check.state),
            color: tunCheckColor(check.state),
            tint: Tokens.idleTint,
            showDot: true,
          ),
        ]),
      );
}
