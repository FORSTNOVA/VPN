part of '../main.dart';

/// What the tunnel is carrying: the session totals and rate, and the connections
/// behind them. A connection whose outbound is DIRECT is called out, because
/// that is the one row worth noticing when the tunnel is supposed to carry
/// everything.
extension _TrafficPage on _HomePageState {
  Widget _trafficPage() => _PageBody(children: [
        _SectionHeader(
          title: '流量与连接',
          trailing: Row(children: [
            _AkHoverButtonWrap(
              color: Tokens.cyan,
              child: OutlinedButton.icon(
                onPressed: _port == null
                    ? null
                    : () {
                        _refresh(() => _trafficPaused = !_trafficPaused);
                        _syncTrafficPoll();
                      },
                icon: Icon(
                    _trafficPaused
                        ? Icons.play_arrow_rounded
                        : Icons.pause_rounded,
                    size: 16),
                label: Text(_trafficPaused ? '继续' : '暂停'),
              ),
            ),
            const SizedBox(width: 10),
            _AkHoverButtonWrap(
              color: Tokens.cyan,
              child: OutlinedButton.icon(
                onPressed: _port == null ? null : () => unawaited(_loadTraffic()),
                icon: const Icon(Icons.refresh_rounded, size: 16),
                label: const Text('刷新'),
              ),
            ),
          ]),
        ),
        const Text(
          '数据来自内核自己维护的连接表：总量是本次内核运行以来的累计值，速率取相邻两次采样的差值。'
          '「节点」一列是承载这条连接的出站，显示 DIRECT 的那条没有经过隧道。'
          '上行/下行是单条连接的字节数。',
          style: Tokens.small,
        ),
        const SizedBox(height: 16),
        if (!_traffic.available)
          _Panel(
            child: _EmptyState(
              text: _traffic.reason.isEmpty
                  ? '还没有流量数据。连接之后本页会每 2 秒采样一次。'
                  : _traffic.reason,
            ),
          )
        else ...[
          _Panel(
            child:
                Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Row(children: [
                const Text('实时速率', style: Tokens.hint),
                const SizedBox(width: 10),
                _StatusDot(color: _trafficPaused ? Tokens.idle : Tokens.ok),
                const SizedBox(width: 6),
                Text(_trafficPaused ? '已暂停采样' : '每 2 秒采样', style: Tokens.hint),
              ]),
              const SizedBox(height: 14),
              _Stats(items: [
                ('上传', rateLabel(_traffic.uploadRate)),
                ('下载', rateLabel(_traffic.downloadRate)),
                ('累计上传', byteLabel(_traffic.uploadTotal)),
                ('累计下载', byteLabel(_traffic.downloadTotal)),
                ('连接数', '${_traffic.connectionCount}'),
              ]),
            ]),
          ),
          const SizedBox(height: 22),
          _SectionHeader(
            title: '活动连接',
            trailing: _Badge(
                label: _traffic.shownCount < _traffic.connectionCount
                    ? '显示流量最大的 ${_traffic.shownCount} 条'
                    : '共 ${_traffic.connectionCount} 条',
                color: Tokens.inkMuted,
                tint: Tokens.idleTint),
          ),
          if (_traffic.connections.isEmpty)
            const _Panel(
              child: _EmptyState(text: '当前没有经过内核的连接。空闲时就会这样，属正常。'),
            )
          else
            _Panel(
              padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 8),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  for (final connection in _traffic.connections)
                    _connectionRow(connection),
                ],
              ),
            ),
        ],
      ]);

  Widget _connectionRow(ConnectionRow connection) {
    final kind = connection.type.isEmpty
        ? connection.network
        : '${connection.network}/${connection.type}';
    final details = [
      if (connection.rule.isNotEmpty) connection.rule,
      if (connection.chain.isNotEmpty)
        connection.chain == 'DIRECT'
            ? 'DIRECT（未走隧道）'
            : connection.chain,
      if (kind.isNotEmpty) kind,
      durationLabel(connection.durationMs),
    ].join(' · ');

    final process = Text(
      connection.process.isEmpty ? '未知应用' : connection.process,
      overflow: TextOverflow.ellipsis,
      style: TextStyle(
          fontSize: 13,
          fontWeight: FontWeight.w500,
          color:
              connection.process.isEmpty ? Tokens.inkFaint : Tokens.ink),
    );
    final target = Text(
      connection.target.isEmpty ? '未知目标' : connection.target,
      overflow: TextOverflow.ellipsis,
      style: const TextStyle(fontSize: 13),
    );
    final up = Text('↑${byteLabel(connection.upload)}', style: Tokens.number);
    final down =
        Text('↓${byteLabel(connection.download)}', style: Tokens.number);
    // A phone has room for the names or for the numbers, not for both on one
    // line: the row turns into a stack rather than pushing the byte counts off
    // the screen.
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    final watermark = connection.chain == 'DIRECT'
        ? 'DIRECT'
        : (connection.network.isNotEmpty ? connection.network.toUpperCase() : 'TCP');
    final previewTag = connection.type.isNotEmpty ? connection.type.toUpperCase() : 'FLOW';

    return _AkHoverRow(
      watermark: watermark,
      previewTag: previewTag,
      accentColor: connection.chain == 'DIRECT' ? Tokens.warn : Tokens.cyan,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        if (narrow) ...[
          process,
          const SizedBox(height: 2),
          target,
          const SizedBox(height: 4),
          Row(children: [up, const SizedBox(width: 12), down]),
        ] else
          Row(children: [
            SizedBox(width: 128, child: process),
            const SizedBox(width: 10),
            Expanded(child: target),
            const SizedBox(width: 12),
            up,
            const SizedBox(width: 10),
            down,
          ]),
        const SizedBox(height: 3),
        Text(details, style: Tokens.hint),
      ]),
    );
  }
}
