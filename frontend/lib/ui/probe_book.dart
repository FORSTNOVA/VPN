import 'dart:convert';
import 'package:flutter/material.dart';

class ProbeBook extends ChangeNotifier {
  ProbeBook(this.send);
  final Future<Map<String, dynamic>> Function(List<String>) send;
  final _results = <String, Map<String, dynamic>>{};
  final _pending = <String, Future<void>>{};
  final _errors = <String, String>{};
  String key(List<String> hops) => jsonEncode(hops);
  bool busy(List<String> hops) => _pending.containsKey(key(hops));
  String? label(List<String> hops) {
    final k = key(hops);
    final row = _results[k];
    if (_errors.containsKey(k)) return '测试异常';
    if (row == null) return null;
    final stage = row['failureStage'];
    final failed = stage == null ? '未通过' : '未通过（$stage）';
    if (row['error'] != null) return failed;
    final warm = row['warmDelay'];
    final warmText = warm != null
        ? '$warm ms'
        : row['warmError'] != null
            ? failed
            : '未测';
    return '首次 ${row['delay']} ms · 连接后 $warmText';
  }

  String detail(List<String> hops) {
    final k = key(hops), row = _results[k];
    if (_errors[k] != null) return _errors[k]!;
    if (row == null) return hops.join(' → ');
    final time = DateTime.fromMillisecondsSinceEpoch(row['measured'] as int)
        .toString()
        .split('.')
        .first;
    final ip = row['entryIP'];
    final entry = row['diagnosticsVersion'] == null
        ? ''
        : ip == null
            ? '\n入口地址：未观测到'
            : '\n${row['entryAttempt'] == true ? '尝试入口' : '连接入口'}：$ip (${row['entryFamily']})';
    final stage =
        row['failureStage'] == null ? '' : '\n失败阶段：${row['failureStage']}';
    return '${hops.length} 跳 · ${hops.join(" → ")}\n${row["target"] ?? "https://www.gstatic.com/generate_204"}\n$time$entry$stage\n${row["error"] ?? label(hops)}${row["warmError"] == null ? "" : "\n${row["warmError"]}"}';
  }

  List<List<String>> pathsTo(String name) => _results.values
      .where((r) {
        final h = r['hops'] as List;
        return h.length > 1 && h.last == name;
      })
      .map((r) => (r['hops'] as List).cast<String>())
      .toList();
  int revision = 0;
  void restore(List rows) {
    _results.clear();
    for (final row in rows.cast<Map<String, dynamic>>()) {
      _results[key((row['hops'] as List).cast<String>())] = row;
    }
    notifyListeners();
  }

  Future<void> run(List<String> hops) {
    final k = key(hops);
    final existing = _pending[k];
    if (existing != null) return existing;
    final future = Future<void>.microtask(() async {
      try {
        _results[k] = await send(List.of(hops));
        _errors.remove(k);
      } catch (e) {
        _errors[k] = e.toString().replaceFirst('Exception: ', '');
      } finally {
        _pending.remove(k);
        revision++;
        notifyListeners();
      }
    });
    _pending[k] = future;
    notifyListeners();
    return future;
  }
}

class PathProbeButton extends StatelessWidget {
  const PathProbeButton(
      {super.key, required this.book, required this.hops, required this.title});
  final ProbeBook book;
  final List<String> hops;
  final String title;
  @override
  Widget build(BuildContext context) => ListenableBuilder(
      listenable: book,
      builder: (context, _) {
        final result = book.label(hops);
        return Tooltip(
            message: book.detail(hops),
            child: TextButton(
              onPressed: book.busy(hops) ? null : () => book.run(hops),
              child: Text(
                  '$title${book.busy(hops) ? ' · 测试中' : result != null ? ' · $result' : ''}'),
            ));
      });
}
