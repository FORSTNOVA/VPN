import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'installed_game_picker.dart';

Future<List<String>?> editGameBypass(
  BuildContext context,
  List<String> current,
  bool android,
) {
  return showDialog<List<String>>(
    context: context,
    builder: (context) => _GameBypassDialog(
      current: current,
      android: android,
    ),
  );
}

class _GameBypassDialog extends StatefulWidget {
  final List<String> current;
  final bool android;

  const _GameBypassDialog({
    required this.current,
    required this.android,
  });

  @override
  State<_GameBypassDialog> createState() => _GameBypassDialogState();
}

class _GameBypassDialogState extends State<_GameBypassDialog> {
  late final TextEditingController _controller;

  @override
  void initState() {
    super.initState();
    _controller = TextEditingController(text: widget.current.join('\n'));
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  Future<void> _pickGame() async {
    try {
      if (widget.android) {
        final rows = await const MethodChannel('smartvpn/host')
            .invokeListMethod<Map<Object?, Object?>>('listGames');
        if (!mounted) return;
        final apps =
            (rows ?? []).map((app) => Map<String, String>.from(app)).toList();
        final selection = await showDialog<List<String>>(
          context: context,
          builder: (_) => InstalledGamePicker(
            apps: apps,
            initial: _controller.text
                .split('\n')
                .map((e) => e.trim())
                .where((e) => e.isNotEmpty)
                .toList(),
          ),
        );
        if (mounted && selection != null) {
          _controller.text = selection.join('\n');
        }
        return;
      }
      final selected = await const MethodChannel('smartvpn/lifecycle')
          .invokeMethod<String>('pickGame');
      if (selected == null || !mounted) return;

      final trimmed = selected.trim();
      if (trimmed.isEmpty) return;

      final exists = _controller.text
          .split('\n')
          .any((line) => line.trim().toLowerCase() == trimmed.toLowerCase());
      if (exists) return;
      final current = _controller.text;
      if (current.isEmpty) {
        _controller.text = trimmed;
      } else {
        _controller.text =
            current.endsWith('\n') ? '$current$trimmed' : '$current\n$trimmed';
      }
    } on PlatformException catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(e.message ?? e.toString())),
        );
      }
    }
  }

  void _onSave() {
    final result = _controller.text
        .split('\n')
        .map((line) => line.trim())
        .where((line) => line.isNotEmpty)
        .toList();
    Navigator.of(context).pop(result);
  }

  @override
  Widget build(BuildContext context) {
    final helperText = widget.android
        ? '每行一个应用包名（例如 com.tencent.tmgp.sgame）。注意：名单中的游戏将绕过代理直连，暴露真实网络IP。'
        : '选择游戏实际运行的 .exe，按完整路径直连。也可手填进程名。游戏服务器会看到直连 IP。';

    return AlertDialog(
      scrollable: true,
      title: const Text('游戏直连名单'),
      content: SizedBox(
        width: double.maxFinite,
        child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              OutlinedButton.icon(
                  onPressed: _pickGame,
                  icon: const Icon(Icons.folder_open),
                  label: Text(widget.android ? '选择已安装的游戏' : '选择游戏本体 (.exe)')),
              const SizedBox(height: 8),
              TextField(
                controller: _controller,
                keyboardType: TextInputType.multiline,
                maxLines: 10,
                minLines: 5,
                decoration: InputDecoration(
                  border: const OutlineInputBorder(),
                  hintText:
                      widget.android ? 'com.example.game' : 'StarRail.exe',
                  helperText: helperText,
                  helperMaxLines: 3,
                ),
              ),
            ]),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(null),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: _onSave,
          child: const Text('保存'),
        ),
      ],
    );
  }
}
