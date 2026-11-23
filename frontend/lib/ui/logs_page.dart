import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'probe_book.dart';
import '../tokens.dart';

enum LogCategory {
  all('全部'),
  probe('代理链与测速'),
  event('节点切换'),
  kernel('内核日志'),
  service('服务日志');

  const LogCategory(this.label);
  final String label;
}

class _LogEntry {
  const _LogEntry({
    required this.category,
    required this.badge,
    required this.badgeColor,
    required this.title,
    required this.subtitle,
    this.time = '',
  });

  final LogCategory category;
  final String badge;
  final Color badgeColor;
  final String title;
  final String subtitle;
  final String time;
}

class LogsPage extends StatefulWidget {
  const LogsPage({
    super.key,
    required this.probes,
    required this.fetchLogs,
    this.refreshTick = 0,
  });

  final ProbeBook probes;
  final Future<Map<String, dynamic>> Function() fetchLogs;
  final int refreshTick;

  @override
  State<LogsPage> createState() => _LogsPageState();
}

class _LogsPageState extends State<LogsPage> {
  final TextEditingController _search = TextEditingController();
  LogCategory _category = LogCategory.all;
  bool _loading = false;
  String? _error;
  List<Map<String, dynamic>> _events = const [];
  List<String> _kernelLogs = const [];
  List<String> _serviceLogs = const [];

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void didUpdateWidget(covariant LogsPage oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.refreshTick != widget.refreshTick) {
      _load();
    }
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    if (_loading) return;
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final data = await widget.fetchLogs();
      if (!mounted) return;
      setState(() {
        _events = (data['events'] as List? ?? const [])
            .whereType<Map>()
            .map((e) => Map<String, dynamic>.from(e))
            .toList();
        _kernelLogs = (data['kernelLogs'] as List? ?? const [])
            .map((e) => e.toString())
            .toList()
            .reversed
            .toList();
        _serviceLogs = (data['serviceLogs'] as List? ?? const [])
            .map((e) => e.toString())
            .toList()
            .reversed
            .toList();
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.toString().replaceFirst('Exception: ', '');
      });
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }

  List<_LogEntry> _entries() {
    final items = <_LogEntry>[];
    for (final row in widget.probes.allRows) {
      final hops = (row['hops'] as List? ?? const []).cast<String>();
      if (hops.isEmpty) continue;
      final isChain = hops.length > 1;
      final ok = row['error'] == null && row['warmError'] == null;
      final measured = row['measured'] as int?;
      final ts = measured == null
          ? ''
          : DateTime.fromMillisecondsSinceEpoch(measured)
              .toString()
              .split('.')
              .first;
      final label = widget.probes.label(hops) ?? '';
      final entryIp = row['entryIP'];
      final ipText = entryIp == null
          ? ''
          : '入口 $entryIp (${row['entryFamily'] ?? ''})';
      final errText = row['error'] ?? row['warmError'] ?? '';
      final details = [
        if (label.isNotEmpty) label,
        if (ipText.isNotEmpty) ipText,
        if (errText is String && errText.isNotEmpty && !label.contains(errText))
          errText,
      ].join(' · ');
      items.add(_LogEntry(
        category: LogCategory.probe,
        badge: isChain ? '代理链' : '单节点',
        badgeColor: ok ? Tokens.ok : Tokens.bad,
        title: hops.join(' → '),
        subtitle: details,
        time: ts,
      ));
    }

    for (final ev in _events) {
      final from = ev['from']?.toString() ?? '';
      final to = ev['to']?.toString() ?? '';
      final trigger = ev['trigger']?.toString() ?? '切换';
      final evidence = ev['evidence']?.toString() ?? '';
      final at = ev['at']?.toString() ?? '';
      items.add(_LogEntry(
        category: LogCategory.event,
        badge: trigger,
        badgeColor: Tokens.cyan,
        title: from.isEmpty ? to : '$from → $to',
        subtitle: evidence,
        time: at,
      ));
    }

    for (final line in _kernelLogs) {
      items.add(_LogEntry(
        category: LogCategory.kernel,
        badge: '内核',
        badgeColor: Tokens.yellow,
        title: line,
        subtitle: '',
      ));
    }

    for (final line in _serviceLogs) {
      items.add(_LogEntry(
        category: LogCategory.service,
        badge: '服务',
        badgeColor: Tokens.idle,
        title: line,
        subtitle: '',
      ));
    }

    final query = _search.text.trim().toLowerCase();
    return items.where((item) {
      if (_category != LogCategory.all && item.category != _category) {
        return false;
      }
      if (query.isEmpty) return true;
      return item.title.toLowerCase().contains(query) ||
          item.subtitle.toLowerCase().contains(query) ||
          item.badge.toLowerCase().contains(query);
    }).toList();
  }

  Future<void> _copyLogs(List<_LogEntry> visible) async {
    final text = visible
        .map((e) => [
              if (e.time.isNotEmpty) '[${e.time}]',
              '[${e.badge}]',
              e.title,
              if (e.subtitle.isNotEmpty) '- ${e.subtitle}',
            ].join(' '))
        .join('\n');
    await Clipboard.setData(ClipboardData(text: text));
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text('已复制 ${visible.length} 条日志')),
    );
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: widget.probes,
      builder: (context, _) {
        final visible = _entries();
        return ListView(
          padding: const EdgeInsets.all(16),
          children: [
            Row(
              children: [
                const Expanded(
                  child: Text(
                    '日志中心',
                    style: TextStyle(fontSize: 18, fontWeight: FontWeight.w700),
                  ),
                ),
                IconButton(
                  tooltip: '复制当前日志',
                  onPressed: visible.isEmpty ? null : () => _copyLogs(visible),
                  icon: const Icon(Icons.copy_all_rounded, size: 20),
                ),
                IconButton(
                  tooltip: '刷新日志',
                  onPressed: _loading ? null : _load,
                  icon: _loading
                      ? const SizedBox(
                          width: 18,
                          height: 18,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Icon(Icons.refresh_rounded, size: 20),
                ),
              ],
            ),
            const SizedBox(height: 8),
            TextField(
              controller: _search,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                isDense: true,
                hintText: '搜索节点、链路、报错或关键词',
                prefixIcon: const Icon(Icons.search, size: 18),
                suffixIcon: _search.text.isEmpty
                    ? null
                    : IconButton(
                        icon: const Icon(Icons.clear, size: 18),
                        onPressed: () => setState(_search.clear),
                      ),
              ),
            ),
            const SizedBox(height: 10),
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: Row(
                children: [
                  for (final cat in LogCategory.values)
                    Padding(
                      padding: const EdgeInsets.only(right: 6),
                      child: ChoiceChip(
                        label: Text(cat.label),
                        selected: _category == cat,
                        onSelected: (_) => setState(() => _category = cat),
                      ),
                    ),
                ],
              ),
            ),
            if (_error != null) ...[
              const SizedBox(height: 8),
              Text(_error!, style: const TextStyle(color: Tokens.bad)),
            ],
            const SizedBox(height: 10),
            if (visible.isEmpty)
              const Padding(
                padding: EdgeInsets.symmetric(vertical: 32),
                child: Center(
                  child: Text('暂无匹配的日志或测速记录', style: Tokens.hint),
                ),
              )
            else
              for (final item in visible)
                Container(
                  margin: const EdgeInsets.only(bottom: 8),
                  padding: const EdgeInsets.all(10),
                  decoration: BoxDecoration(
                    color: Tokens.surface,
                    borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                    border: Border.all(color: Tokens.hairline),
                  ),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Container(
                            padding: const EdgeInsets.symmetric(
                                horizontal: 6, vertical: 2),
                            decoration: BoxDecoration(
                              color: item.badgeColor.withValues(alpha: 0.16),
                              borderRadius: BorderRadius.circular(4),
                              border: Border.all(
                                  color:
                                      item.badgeColor.withValues(alpha: 0.5)),
                            ),
                            child: Text(
                              item.badge,
                              style: TextStyle(
                                fontSize: 11,
                                fontWeight: FontWeight.w600,
                                color: item.badgeColor,
                              ),
                            ),
                          ),
                          if (item.time.isNotEmpty) ...[
                            const SizedBox(width: 8),
                            Expanded(
                              child: Text(
                                item.time,
                                style: Tokens.hint,
                                overflow: TextOverflow.ellipsis,
                              ),
                            ),
                          ],
                        ],
                      ),
                      const SizedBox(height: 6),
                      SelectableText(
                        item.title,
                        style: const TextStyle(fontSize: 13),
                      ),
                      if (item.subtitle.isNotEmpty) ...[
                        const SizedBox(height: 3),
                        SelectableText(
                          item.subtitle,
                          style: Tokens.hint,
                        ),
                      ],
                    ],
                  ),
                ),
          ],
        );
      },
    );
  }
}
