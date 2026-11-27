import 'dart:convert';
import 'package:flutter/material.dart';

class ProbeBook extends ChangeNotifier {
  ProbeBook(this.send, {this.sendPolicy});
  final Future<Map<String, dynamic>> Function(
      List<String>, Map<String, List<String>>)? sendPolicy;
  final _scopes = <String, ProbeBook>{};
  ProbeBook forBackups(Map<String, List<String>> backups) {
    if (backups.isEmpty) return this;
    final members = {
      for (final e in backups.entries) e.key: List<String>.of(e.value)
    };
    return _scopes.putIfAbsent(
        jsonEncode(members),
        () => ProbeBook((hops) {
              final subset = {
                for (final name in hops)
                  if (members.containsKey(name)) name: members[name]!
              };
              return subset.isEmpty || hops.length == 1
                  ? send(hops)
                  : sendPolicy!(hops, subset);
            }));
  }

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

  List<Map<String, dynamic>> get allRows {
    final rows = _results.values.toList();
    rows.sort((a, b) =>
        ((b['measured'] as int?) ?? 0).compareTo((a['measured'] as int?) ?? 0));
    return rows;
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
      if (row['backups'] case final Map raw when raw.isNotEmpty) {
        final book = forBackups(raw
            .map((k, v) => MapEntry(k as String, (v as List).cast<String>())));
        book._results[key((row['hops'] as List).cast<String>())] = row;
        book.notifyListeners();
        continue;
      }
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
        debugPrint('ProbeBook.run error ($k): $e');
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

typedef PathBackups = Map<String, List<String>>;

class SpeedBook extends ChangeNotifier {
  SpeedBook(this.send);
  final Future<Map<String, dynamic>> Function(List<String>, PathBackups) send;
  final _results = <String, Map<String, dynamic>>{};
  final _pending = <String>{};

  String key(List<String> hops, PathBackups backups) => jsonEncode([
        hops,
        {
          for (final hop in hops)
            if (backups.containsKey(hop)) hop: backups[hop]
        }
      ]);

  String? label(String path) {
    if (_pending.contains(path)) return '测速中';
    final row = _results[path];
    if (row == null) return null;
    if (row['error'] != null) return '未通过';
    return '${(row['downMbps'] as num).toStringAsFixed(1)} Mbps';
  }

  String detail(String path) {
    final row = _results[path];
    if (row == null) return '通过此路径下载测速';
    return row['error'] as String? ??
        '${row["target"]} · ${row["downBytes"]} 字节 · ${row["downMs"]} ms';
  }

  Future<void> run(List<String> hops, PathBackups backups) async {
    final path = key(hops, backups);
    if (!_pending.add(path)) return;
    notifyListeners();
    try {
      final row = await send(List.of(hops), {
        for (final hop in hops)
          if (backups.containsKey(hop)) hop: List.of(backups[hop]!)
      });
      if (row['error'] == null &&
          !(row['downMbps'] is num &&
              row['downBytes'] is int &&
              row['downMs'] is int &&
              row['target'] is String)) {
        throw const FormatException('下行测速结果缺少测量数据');
      }
      _results[path] = row;
    } catch (e) {
      _results[path] = {'error': e.toString()};
    } finally {
      _pending.remove(path);
      notifyListeners();
    }
  }
}

class PathSpeedButton extends StatelessWidget {
  const PathSpeedButton(
      {super.key,
      required this.book,
      required this.hops,
      this.backups = const {}});
  final SpeedBook book;
  final List<String> hops;
  final PathBackups backups;

  @override
  Widget build(BuildContext context) => ListenableBuilder(
        listenable: book,
        builder: (_, __) {
          final path = book.key(hops, backups);
          return Tooltip(
              message: book.detail(path),
              child: TextButton(
                onPressed: hops.isEmpty || book._pending.contains(path)
                    ? null
                    : () => book.run(hops, backups),
                child: Text(book.label(path) ?? '测带宽'),
              ));
        },
      );
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
