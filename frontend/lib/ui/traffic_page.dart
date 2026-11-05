part of '../main.dart';

/// The Rhodes Island Telemetry & Connections Monitor (流量 / 连接).
/// Features:
/// 1. Persisted historical traffic statistics (时段/日期/应用汇总/连接明细/分页钻取)
/// 2. Real-time active connection monitor (活跃连接流、实时带宽速率)
extension _TrafficPage on _HomePageState {
  Widget _trafficPage() {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;

    return _PageBody(children: [
      _historySection(narrow: narrow),
      const SizedBox(height: 28),
      _liveTrafficSection(narrow: narrow),
    ]);
  }

  // ---------------------------------------------------------------------------
  // Traffic History Controller & Operations
  // ---------------------------------------------------------------------------

  Future<void> _loadTrafficHistory() async {
    final generation = ++_historyRequestSeq;
    if (_port == null) {
      if (mounted) {
        _refresh(() {
          _historyLoading = false;
          _historyData = null;
          _historyError = '本地服务尚未就绪';
        });
      }
      return;
    }

    _refresh(() {
      _historyLoading = true;
      _historyError = null;
      _historyData = null;
    });

    try {
      final dateStr = formatTrafficHistoryDate(
        period: _historyPeriod,
        date: _historyDate,
        hour: _historyHour,
      );
      final route = buildTrafficHistoryRoute(
        period: _historyPeriod,
        date: dateStr,
        group: _historyGroup,
        appKey: _historyFilterAppKey,
        offset: _historyOffset,
        limit: _HomePageState._historyLimit,
      );
      final response = await _request('GET', route);
      if (!mounted || generation != _historyRequestSeq) return;

      _refresh(() {
        _historyData = TrafficHistoryResponse.fromJson(response);
        _historyLoading = false;
        _historyError = null;
      });
    } catch (e) {
      if (!mounted || generation != _historyRequestSeq) return;
      final raw = e.toString().replaceFirst('Exception: ', '').trim();
      final readable = raw.contains('404')
          ? '历史流量接口尚未就绪或本地服务版本较低 (404)'
          : (raw.isEmpty ? '获取历史流量数据失败' : raw);
      _refresh(() {
        _historyLoading = false;
        _historyData = null; // errors must not show stale period totals
        _historyError = readable;
      });
    }
  }

  void _shiftHistoryPeriod(int direction) {
    final current = DateTime(
        _historyDate.year, _historyDate.month, _historyDate.day, _historyHour);
    final next = switch (_historyPeriod) {
      TrafficHistoryPeriod.hour => current.add(Duration(hours: direction)),
      TrafficHistoryPeriod.day =>
        DateTime(current.year, current.month, current.day + direction),
      TrafficHistoryPeriod.month =>
        DateTime(current.year, current.month + direction, 1),
      TrafficHistoryPeriod.year => DateTime(current.year + direction, 1, 1),
    };
    if (next.year < 1 || next.year > 9999) return;
    _refresh(() {
      _historyDate = next;
      _historyHour = next.hour;
      _historyOffset = 0;
    });
    unawaited(_loadTrafficHistory());
  }

  void _resetHistoryPeriodToNow() {
    final now = DateTime.now();
    _refresh(() {
      _historyDate = now;
      _historyHour = now.hour;
      _historyOffset = 0;
    });
    unawaited(_loadTrafficHistory());
  }

  Future<void> _pickHistoryDate() async {
    final picked = await showDatePicker(
      context: context,
      initialDate: _historyDate,
      firstDate: DateTime(1),
      lastDate: DateTime(9999, 12, 31),
      initialDatePickerMode: _historyPeriod == TrafficHistoryPeriod.year
          ? DatePickerMode.year
          : DatePickerMode.day,
      helpText: switch (_historyPeriod) {
        TrafficHistoryPeriod.hour => '选择统计日期',
        TrafficHistoryPeriod.day => '选择统计日',
        TrafficHistoryPeriod.month => '选择统计月份',
        TrafficHistoryPeriod.year => '选择统计年份',
      },
      cancelText: '取消',
      confirmText: '确定',
    );
    if (picked != null && mounted) {
      _refresh(() {
        _historyDate = switch (_historyPeriod) {
          TrafficHistoryPeriod.hour ||
          TrafficHistoryPeriod.day =>
            DateTime(picked.year, picked.month, picked.day),
          TrafficHistoryPeriod.month => DateTime(picked.year, picked.month, 1),
          TrafficHistoryPeriod.year => DateTime(picked.year, 1, 1),
        };
        _historyOffset = 0;
      });
      unawaited(_loadTrafficHistory());
    }
  }

  String _historyDateDisplay() {
    String two(int v) => v.toString().padLeft(2, '0');
    return switch (_historyPeriod) {
      TrafficHistoryPeriod.hour =>
        '${_historyDate.year}-${two(_historyDate.month)}-${two(_historyDate.day)}',
      TrafficHistoryPeriod.day =>
        '${_historyDate.year}-${two(_historyDate.month)}-${two(_historyDate.day)}',
      TrafficHistoryPeriod.month =>
        '${_historyDate.year}年 ${two(_historyDate.month)}月',
      TrafficHistoryPeriod.year => '${_historyDate.year}年',
    };
  }

  // ---------------------------------------------------------------------------
  // Traffic History UI Components
  // ---------------------------------------------------------------------------

  Widget _historySection({required bool narrow}) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _SectionHeader(
          title: '历史流量统计',
          subtitle: 'TRAFFIC_HISTORY',
          trailing: Wrap(
            spacing: 8,
            runSpacing: 6,
            children: [
              OutlinedButton.icon(
                onPressed: _resetHistoryPeriodToNow,
                icon: const Icon(Icons.today_rounded, size: 16),
                label: const Text('当前时段'),
              ),
              OutlinedButton.icon(
                onPressed: _historyLoading
                    ? null
                    : () => unawaited(_loadTrafficHistory()),
                icon: const Icon(Icons.refresh_rounded, size: 16),
                label: const Text('刷新'),
              ),
            ],
          ),
        ),
        _Panel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Wrap(
                spacing: 14,
                runSpacing: 12,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  _historyPeriodSegmentedButton(),
                  _historyGroupSegmentedButton(),
                  _historyDateControls(),
                ],
              ),

              // Filter badge / back button when filtering by appKey
              if (_historyFilterAppKey != null) ...[
                const SizedBox(height: 12),
                Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
                  decoration: BoxDecoration(
                    color: Tokens.yellowTint,
                    borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                    border:
                        Border.all(color: Tokens.yellow.withValues(alpha: 0.5)),
                  ),
                  child: Row(
                    children: [
                      const Icon(Icons.filter_alt_rounded,
                          size: 16, color: Tokens.yellow),
                      const SizedBox(width: 8),
                      Expanded(
                        child: Text(
                          '已按应用过滤：${_historyFilterAppName ?? _historyFilterAppKey}',
                          style: const TextStyle(
                            fontSize: 12,
                            fontWeight: FontWeight.w700,
                            color: Tokens.yellow,
                          ),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                      TextButton.icon(
                        style: TextButton.styleFrom(
                          padding: const EdgeInsets.symmetric(
                              horizontal: 8, vertical: 4),
                          minimumSize: Size.zero,
                          tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                        ),
                        onPressed: () {
                          _refresh(() {
                            _historyFilterAppKey = null;
                            _historyFilterAppName = null;
                            _historyOffset = 0;
                            _historyGroup = TrafficHistoryGroup.app;
                          });
                          unawaited(_loadTrafficHistory());
                        },
                        icon: const Icon(Icons.close_rounded,
                            size: 14, color: Tokens.yellow),
                        label: const Text('重置过滤并返回',
                            style:
                                TextStyle(fontSize: 12, color: Tokens.yellow)),
                      ),
                    ],
                  ),
                ),
              ],

              const SizedBox(height: 14),
              const Divider(height: 1),
              const SizedBox(height: 14),

              // Summary Stats (only when data available and no error)
              if (_historyData != null && _historyError == null) ...[
                _Stats(items: [
                  ('累计上传', byteLabel(_historyData!.uploadTotal)),
                  ('累计下载', byteLabel(_historyData!.downloadTotal)),
                  ('总计流量', byteLabel(_historyData!.totalBytes)),
                  ('总连接数', '${_historyData!.connectionCount}'),
                  (
                    _historyGroup == TrafficHistoryGroup.app ? '应用数' : '连接数',
                    '${_historyData!.rowCount}',
                  ),
                ]),
                const SizedBox(height: 8),
                Text('本机时区 ${_historyData!.timezone}', style: Tokens.hint),
                const SizedBox(height: 14),
              ],

              // Sampling error notice if any
              if (_historyData?.samplingError != null &&
                  _historyData!.samplingError!.isNotEmpty) ...[
                _Notice(
                  text: '采样提示：${_historyData!.samplingError}',
                  kind: _NoticeKind.warn,
                ),
                const SizedBox(height: 12),
              ],

              // Content: Loading / Error / Empty / Rows
              if (_historyLoading && _historyData == null)
                const Padding(
                  padding: EdgeInsets.symmetric(vertical: 24),
                  child: Center(
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        SizedBox(
                          width: 24,
                          height: 24,
                          child: CircularProgressIndicator(
                            strokeWidth: 2.5,
                            color: Tokens.yellow,
                          ),
                        ),
                        SizedBox(height: 12),
                        Text('正在读取历史流量数据…', style: Tokens.hint),
                      ],
                    ),
                  ),
                )
              else if (_historyError != null)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 8),
                  child: _Notice(
                    text: '读取历史流量失败：$_historyError',
                    kind: _NoticeKind.bad,
                    action: OutlinedButton(
                      onPressed: () => unawaited(_loadTrafficHistory()),
                      child: const Text('重试'),
                    ),
                  ),
                )
              else if (_historyData == null || _historyData!.rows.isEmpty)
                _EmptyState(
                  text: _port == null
                      ? '本地服务尚未就绪。启动服务后即可查询历史流量。'
                      : '所选时段暂无历史流量记录。经过 SmartVPN 内核的流量将在此归档。',
                  actionLabel: _port != null ? '刷新数据' : null,
                  onAction: _port != null
                      ? () => unawaited(_loadTrafficHistory())
                      : null,
                )
              else ...[
                Container(
                  decoration: BoxDecoration(
                    color: Tokens.surfaceElevated.withValues(alpha: 0.4),
                    borderRadius: BorderRadius.circular(Tokens.radiusControl),
                    border: Border.all(color: Tokens.hairline),
                  ),
                  child: Column(
                    children: [
                      for (final row in _historyData!.rows)
                        _historyGroup == TrafficHistoryGroup.app
                            ? _historyAppRow(row)
                            : _historyConnectionRow(row),
                    ],
                  ),
                ),
                const SizedBox(height: 10),
                _historyPaginationBar(totalRows: _historyData!.rowCount),
              ],

              // Accuracy note
              Container(
                margin: const EdgeInsets.only(top: 14),
                padding:
                    const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                decoration: BoxDecoration(
                  color: Tokens.surfaceElevated,
                  borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                  border: Border.all(color: Tokens.hairline),
                ),
                child: const Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(Icons.info_outline_rounded,
                        size: 14, color: Tokens.inkMuted),
                    SizedBox(width: 8),
                    Expanded(
                      child: Text(
                        '每秒采样并保存已观测连接流量；短连接或异常退出可能遗漏。跨时段增量按采样时间归档。仅统计经过 SmartVPN 内核的流量。',
                        style: TextStyle(
                            fontSize: 11, color: Tokens.inkMuted, height: 1.4),
                      ),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ],
    );
  }

  Widget _historyPeriodSegmentedButton() =>
      SegmentedButton<TrafficHistoryPeriod>(
        segments: const [
          ButtonSegment(
            value: TrafficHistoryPeriod.hour,
            label: Text('小时'),
          ),
          ButtonSegment(
            value: TrafficHistoryPeriod.day,
            label: Text('日'),
          ),
          ButtonSegment(
            value: TrafficHistoryPeriod.month,
            label: Text('月'),
          ),
          ButtonSegment(
            value: TrafficHistoryPeriod.year,
            label: Text('年'),
          ),
        ],
        selected: {_historyPeriod},
        onSelectionChanged: (set) {
          _refresh(() {
            _historyPeriod = set.first;
            _historyOffset = 0;
          });
          unawaited(_loadTrafficHistory());
        },
      );

  Widget _historyGroupSegmentedButton() => SegmentedButton<TrafficHistoryGroup>(
        segments: const [
          ButtonSegment(
            value: TrafficHistoryGroup.app,
            label: Text('应用汇总'),
            icon: Icon(Icons.apps_rounded, size: 15),
          ),
          ButtonSegment(
            value: TrafficHistoryGroup.connection,
            label: Text('连接明细'),
            icon: Icon(Icons.cable_rounded, size: 15),
          ),
        ],
        selected: {_historyGroup},
        onSelectionChanged: (set) {
          _refresh(() {
            _historyGroup = set.first;
            _historyOffset = 0;
          });
          unawaited(_loadTrafficHistory());
        },
      );

  Widget _historyDateControls() => Wrap(
        spacing: 8,
        runSpacing: 8,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Container(
            decoration: BoxDecoration(
              color: Tokens.surfaceElevated,
              borderRadius: BorderRadius.circular(Tokens.radiusControl),
              border: Border.all(color: Tokens.hairline),
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                IconButton(
                  tooltip: '上一时段',
                  icon: const Icon(Icons.chevron_left_rounded, size: 18),
                  onPressed: () => _shiftHistoryPeriod(-1),
                  visualDensity: VisualDensity.compact,
                  padding: const EdgeInsets.all(6),
                  constraints: const BoxConstraints(),
                ),
                InkWell(
                  onTap: _pickHistoryDate,
                  child: Padding(
                    padding:
                        const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        const Icon(Icons.calendar_today_rounded,
                            size: 13, color: Tokens.yellow),
                        const SizedBox(width: 6),
                        Text(
                          _historyDateDisplay(),
                          style: const TextStyle(
                            fontSize: 12.5,
                            fontWeight: FontWeight.w600,
                            color: Tokens.ink,
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
                IconButton(
                  tooltip: '下一时段',
                  icon: const Icon(Icons.chevron_right_rounded, size: 18),
                  onPressed: () => _shiftHistoryPeriod(1),
                  visualDensity: VisualDensity.compact,
                  padding: const EdgeInsets.all(6),
                  constraints: const BoxConstraints(),
                ),
              ],
            ),
          ),
          if (_historyPeriod == TrafficHistoryPeriod.hour) _hourPickerWidget(),
        ],
      );

  Widget _hourPickerWidget() {
    String two(int v) => v.toString().padLeft(2, '0');
    return PopupMenuButton<int>(
      tooltip: '选择时段（0-23时）',
      initialValue: _historyHour,
      onSelected: (hour) {
        _refresh(() {
          _historyHour = hour;
          _historyOffset = 0;
        });
        unawaited(_loadTrafficHistory());
      },
      itemBuilder: (context) => [
        for (var h = 0; h < 24; h++)
          PopupMenuItem<int>(
            value: h,
            child: Row(
              children: [
                if (h == _historyHour)
                  const Icon(Icons.check_rounded,
                      size: 16, color: Tokens.yellow)
                else
                  const SizedBox(width: 16),
                const SizedBox(width: 8),
                Text(
                  '${two(h)}:00 - ${two(h)}:59',
                  style: TextStyle(
                    fontFamily: 'Consolas',
                    fontFamilyFallback: Tokens.fontMonoFallback,
                    fontWeight:
                        h == _historyHour ? FontWeight.w700 : FontWeight.w500,
                    color: h == _historyHour ? Tokens.yellow : Tokens.ink,
                  ),
                ),
              ],
            ),
          ),
      ],
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
        decoration: BoxDecoration(
          color: Tokens.surfaceElevated,
          borderRadius: BorderRadius.circular(Tokens.radiusControl),
          border: Border.all(color: Tokens.hairline),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.schedule_rounded, size: 14, color: Tokens.yellow),
            const SizedBox(width: 6),
            Text(
              '${two(_historyHour)}:00 - ${two(_historyHour)}:59',
              style: const TextStyle(
                fontFamily: 'Consolas',
                fontFamilyFallback: Tokens.fontMonoFallback,
                fontSize: 12.5,
                fontWeight: FontWeight.w600,
                color: Tokens.yellow,
              ),
            ),
            const SizedBox(width: 4),
            const Icon(Icons.arrow_drop_down_rounded,
                size: 16, color: Tokens.inkMuted),
          ],
        ),
      ),
    );
  }

  Widget _historyAppRow(TrafficHistoryRow row) {
    final isUnknown = row.isUnknownApp;
    final title = Text(
      row.displayAppName,
      maxLines: 1,
      overflow: TextOverflow.ellipsis,
      style: TextStyle(
        fontSize: 13,
        fontWeight: FontWeight.w700,
        color: isUnknown ? Tokens.inkMuted : Tokens.ink,
      ),
    );

    final up = Text('↑ ${byteLabel(row.upload)}',
        style: Tokens.number.copyWith(fontSize: 12, color: Tokens.inkMuted));
    final down = Text('↓ ${byteLabel(row.download)}',
        style: Tokens.number.copyWith(fontSize: 12, color: Tokens.cyan));
    final countBadge = _Badge(
      label: '${row.connections} 连接',
      color: Tokens.yellow,
      tint: Tokens.yellowTint,
    );
    final totalText = Text(
      '总量 ${byteLabel(row.totalBytes)}',
      style: Tokens.hint.copyWith(fontSize: 11),
    );

    return _AkHoverRow(
      accentColor: Tokens.yellow,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 9),
      onTap: () {
        _refresh(() {
          _historyFilterAppKey = row.appKey.isNotEmpty ? row.appKey : row.app;
          _historyFilterAppName = row.displayAppName;
          _historyGroup = TrafficHistoryGroup.connection;
          _historyOffset = 0;
        });
        unawaited(_loadTrafficHistory());
      },
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: title),
              countBadge,
            ],
          ),
          if (row.appKey.isNotEmpty && row.appKey != row.app) ...[
            const SizedBox(height: 2),
            Text(row.appKey,
                style: Tokens.hint, overflow: TextOverflow.ellipsis),
          ],
          const SizedBox(height: 5),
          Wrap(
            spacing: 10,
            runSpacing: 6,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              up,
              down,
              totalText,
              const SizedBox(width: 6),
              const Icon(Icons.chevron_right_rounded,
                  size: 16, color: Tokens.inkMuted),
            ],
          ),
        ],
      ),
    );
  }

  Widget _historyConnectionRow(TrafficHistoryRow row) {
    final isDirect = row.chain.toUpperCase() == 'DIRECT';
    final idBadge = Tooltip(
        message: row.id,
        child: _Badge(
          label: row.id.isEmpty
              ? '#—'
              : '#${row.id.substring(0, math.min(10, row.id.length))}',
          color: Tokens.yellow,
          tint: Tokens.yellowTint,
        ));
    final isUnknown = row.isUnknownApp;
    final appText = Text(
      row.displayAppName,
      maxLines: 1,
      overflow: TextOverflow.ellipsis,
      style: TextStyle(
        fontSize: 12.5,
        fontWeight: FontWeight.w600,
        color: isUnknown ? Tokens.inkMuted : Tokens.ink,
      ),
    );
    final targetText = Text(
      row.target.isNotEmpty ? row.target : '未知目标',
      overflow: TextOverflow.ellipsis,
      style: TextStyle(
        fontSize: 12,
        fontFamily: 'Consolas',
        fontFamilyFallback: Tokens.fontMonoFallback,
        color: isDirect ? Tokens.warn : Tokens.ink,
      ),
    );
    final protoChain = [
      if (row.network.isNotEmpty) row.network.toUpperCase(),
      if (row.chain.isNotEmpty) (isDirect ? 'DIRECT (直连)' : row.chain),
      if (row.connections > 1) '${row.connections} 连接',
    ].join(' · ');

    final timestamps =
        '首次观测 ${formatHistoryTimestamp(row.firstSeen)}  最近观测 ${formatHistoryTimestamp(row.lastSeen)}';

    final up = Text('↑ ${byteLabel(row.upload)}',
        style: Tokens.number.copyWith(fontSize: 11.5, color: Tokens.inkMuted));
    final down = Text('↓ ${byteLabel(row.download)}',
        style: Tokens.number.copyWith(fontSize: 11.5, color: Tokens.cyan));

    return _AkHoverRow(
      accentColor: isDirect ? Tokens.warn : Tokens.cyan,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              idBadge,
              const SizedBox(width: 8),
              Expanded(child: appText),
              if (isDirect)
                const _Badge(
                  label: 'DIRECT 直连',
                  color: Tokens.warn,
                  tint: Tokens.warnTint,
                ),
            ],
          ),
          const SizedBox(height: 3),
          targetText,
          const SizedBox(height: 4),
          Row(
            children: [
              up,
              const SizedBox(width: 12),
              down,
            ],
          ),
          const SizedBox(height: 4),
          Text(protoChain, style: Tokens.hint),
          const SizedBox(height: 2),
          Text(timestamps, style: Tokens.hint),
        ],
      ),
    );
  }

  Widget _historyPaginationBar({required int totalRows}) {
    final totalPages = totalRows <= 0
        ? 1
        : ((totalRows - 1) ~/ _HomePageState._historyLimit) + 1;
    final currentPage = (_historyOffset ~/ _HomePageState._historyLimit) + 1;
    final hasPrev = _historyOffset > 0;
    final hasNext = (_historyData?.hasMore ?? false) ||
        (_historyOffset + _HomePageState._historyLimit < totalRows);

    return Container(
      padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 4),
      child: Wrap(
        spacing: 12,
        runSpacing: 8,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          OutlinedButton.icon(
            onPressed: hasPrev && !_historyLoading
                ? () {
                    _refresh(() {
                      _historyOffset = math.max(
                          0, _historyOffset - _HomePageState._historyLimit);
                    });
                    unawaited(_loadTrafficHistory());
                  }
                : null,
            icon: const Icon(Icons.chevron_left_rounded, size: 16),
            label: const Text('上一页'),
          ),
          Text(
            '第 $currentPage / $totalPages 页 (共 $totalRows 条)',
            style: Tokens.hint,
          ),
          OutlinedButton.icon(
            onPressed: hasNext && !_historyLoading
                ? () {
                    _refresh(() {
                      _historyOffset =
                          _historyOffset + _HomePageState._historyLimit;
                    });
                    unawaited(_loadTrafficHistory());
                  }
                : null,
            icon: const Icon(Icons.chevron_right_rounded, size: 16),
            label: const Text('下一页'),
          ),
        ],
      ),
    );
  }

  // ---------------------------------------------------------------------------
  // Live Telemetry Section (Existing monitor)
  // ---------------------------------------------------------------------------

  Widget _liveTrafficSection({required bool narrow}) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _SectionHeader(
          title: '实时连接监控',
          subtitle: 'LIVE_SOCKETS',
          trailing: Wrap(
            spacing: 8,
            runSpacing: 6,
            children: [
              OutlinedButton.icon(
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
                  size: 16,
                ),
                label: Text(_trafficPaused ? '继续' : '暂停'),
              ),
              OutlinedButton.icon(
                onPressed:
                    _port == null ? null : () => unawaited(_loadTraffic()),
                icon: const Icon(Icons.refresh_rounded, size: 16),
                label: const Text('刷新'),
              ),
            ],
          ),
        ),
        const Text(
          '数据实时读取自内核活跃连接表：速率取相邻两次采样的差值，总量为内核启动以来的累计值。'
          '出站标记为 DIRECT 的连接直连未经过隧道。',
          style: Tokens.small,
        ),
        const SizedBox(height: 14),

        // Telemetry Cards
        if (!_traffic.available)
          _Panel(
            child: _EmptyState(
              text: _traffic.reason.isEmpty
                  ? '暂无活跃流量数据。连接代理后页面将每 2 秒实时采样。'
                  : _traffic.reason,
            ),
          )
        else ...[
          _Panel(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(children: [
                  _StatusDot(
                    color: _trafficPaused ? Tokens.idle : Tokens.ok,
                    size: 8,
                  ),
                  const SizedBox(width: 8),
                  Text(
                    _trafficPaused ? '采样已暂停' : '实时监控中 (每 2 秒采样)',
                    style: Tokens.small.copyWith(
                      fontWeight: FontWeight.w700,
                      color: _trafficPaused ? Tokens.inkMuted : Tokens.ok,
                    ),
                  ),
                  const Spacer(),
                  const Text('// BANDWIDTH', style: Tokens.akBilingualEn),
                ]),
                const SizedBox(height: 14),
                _Stats(items: [
                  ('上传速率', rateLabel(_traffic.uploadRate)),
                  ('下载速率', rateLabel(_traffic.downloadRate)),
                  ('累计上传', byteLabel(_traffic.uploadTotal)),
                  ('累计下载', byteLabel(_traffic.downloadTotal)),
                  ('活跃连接数', '${_traffic.connectionCount}'),
                ]),
              ],
            ),
          ),
          const SizedBox(height: 22),

          // Live Connections List
          _SectionHeader(
            title: '活动连接列表',
            subtitle: 'ACTIVE_SOCKETS',
            trailing: _Badge(
              label: _traffic.shownCount < _traffic.connectionCount
                  ? '显示前 ${_traffic.shownCount} 条连接'
                  : '共 ${_traffic.connectionCount} 条连接',
              color: Tokens.inkMuted,
              tint: Tokens.idleTint,
            ),
          ),
          if (_traffic.connections.isEmpty)
            const _Panel(
              child: _EmptyState(text: '当前暂无经过内核的活跃连接。系统空闲时属于正常状态。'),
            )
          else
            _Panel(
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  for (final connection in _traffic.connections)
                    _connectionRow(connection, narrow: narrow),
                ],
              ),
            ),
        ],
      ],
    );
  }

  Widget _connectionRow(ConnectionRow connection, {required bool narrow}) {
    final isDirect = connection.chain == 'DIRECT';
    final kind = connection.type.isEmpty
        ? connection.network
        : '${connection.network}/${connection.type}';
    final details = [
      if (connection.rule.isNotEmpty) connection.rule,
      if (connection.chain.isNotEmpty)
        isDirect ? 'DIRECT (直连未走隧道)' : connection.chain,
      if (kind.isNotEmpty) kind,
      durationLabel(connection.durationMs),
    ].join(' · ');

    final processWidget = Text(
      connection.process.isEmpty ? '系统进程' : connection.process,
      overflow: TextOverflow.ellipsis,
      style: TextStyle(
        fontSize: 13,
        fontWeight: FontWeight.w700,
        color: connection.process.isEmpty ? Tokens.inkFaint : Tokens.ink,
      ),
    );

    final targetWidget = Text(
      connection.target.isEmpty ? '未知目标' : connection.target,
      overflow: TextOverflow.ellipsis,
      style: TextStyle(
        fontSize: 12.5,
        fontFamily: 'Consolas',
        fontFamilyFallback: Tokens.fontMonoFallback,
        color: isDirect ? Tokens.warn : Tokens.ink,
      ),
    );

    final up = Text('↑ ${byteLabel(connection.upload)}',
        style: Tokens.number.copyWith(fontSize: 12, color: Tokens.inkMuted));
    final down = Text('↓ ${byteLabel(connection.download)}',
        style: Tokens.number.copyWith(fontSize: 12, color: Tokens.cyan));

    return _AkHoverRow(
      accentColor: isDirect ? Tokens.warn : Tokens.cyan,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 9),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (narrow) ...[
            Row(children: [
              Expanded(child: processWidget),
              if (isDirect)
                const _Badge(
                  label: 'DIRECT 直连',
                  color: Tokens.warn,
                  tint: Tokens.warnTint,
                ),
            ]),
            const SizedBox(height: 3),
            targetWidget,
            const SizedBox(height: 5),
            Row(children: [up, const SizedBox(width: 14), down]),
          ] else ...[
            Row(children: [
              SizedBox(width: 130, child: processWidget),
              const SizedBox(width: 10),
              Expanded(child: targetWidget),
              if (isDirect) ...[
                const SizedBox(width: 8),
                const _Badge(
                  label: 'DIRECT 直连',
                  color: Tokens.warn,
                  tint: Tokens.warnTint,
                ),
              ],
              const SizedBox(width: 14),
              up,
              const SizedBox(width: 12),
              down,
            ]),
          ],
          const SizedBox(height: 4),
          Text(details, style: Tokens.hint),
        ],
      ),
    );
  }
}
