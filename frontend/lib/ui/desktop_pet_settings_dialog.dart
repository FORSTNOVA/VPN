import 'package:flutter/material.dart';

import '../tokens.dart';

/// Redesigned Rhodes Island industrial-styled settings dialog for the native Amiya desktop pet.
class DesktopPetSettingsDialog extends StatefulWidget {
  final bool enabled;
  final double scale;
  final bool autoMiniOnFullscreen;
  final int width;
  final int height;

  const DesktopPetSettingsDialog({
    super.key,
    required this.enabled,
    required this.scale,
    required this.autoMiniOnFullscreen,
    required this.width,
    required this.height,
  });

  @override
  State<DesktopPetSettingsDialog> createState() =>
      _DesktopPetSettingsDialogState();
}

class _DesktopPetSettingsDialogState extends State<DesktopPetSettingsDialog> {
  late bool _enabled;
  late double _scale;
  late bool _autoMini;
  late int _width;
  late int _height;

  @override
  void initState() {
    super.initState();
    _enabled = widget.enabled;
    _scale = widget.scale.clamp(0.6, 1.8);
    _autoMini = widget.autoMiniOnFullscreen;
    _width = widget.width.clamp(260, 560);
    _height = widget.height.clamp(68, 160);
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      backgroundColor: Tokens.surface,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Tokens.radiusPanel),
        side: const BorderSide(color: Tokens.hairline),
      ),
      titlePadding: const EdgeInsets.fromLTRB(20, 16, 20, 10),
      contentPadding: const EdgeInsets.symmetric(horizontal: 20),
      actionsPadding: const EdgeInsets.fromLTRB(20, 6, 20, 14),
      title: Row(
        children: [
          Container(width: 3.5, height: 18, color: Tokens.yellow),
          const SizedBox(width: 8),
          const Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(
                  '阿米娅桌面助理',
                  style: TextStyle(
                      fontSize: 16,
                      fontWeight: FontWeight.w800,
                      color: Tokens.ink,
                      letterSpacing: 0.5),
                ),
                Text(
                  'AMIYA / DESKTOP COMPANION',
                  style: TextStyle(
                      fontSize: 9.5,
                      fontWeight: FontWeight.w700,
                      color: Tokens.cyan,
                      letterSpacing: 1.0),
                ),
              ],
            ),
          ),
        ],
      ),
      content: SizedBox(
        width: 460,
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // Character Card & Behaviour Overview
              Container(
                padding: const EdgeInsets.all(10),
                decoration: BoxDecoration(
                  color: Tokens.surfaceElevated,
                  borderRadius: BorderRadius.circular(Tokens.radiusControl),
                  border: Border.all(color: Tokens.hairline),
                ),
                child: Row(
                  children: [
                    // First cell only (top-left idle holding tablet) of 2x2 sprite sheet
                    Container(
                      decoration: BoxDecoration(
                        color: Tokens.surface,
                        borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                        border: Border.all(color: Tokens.hairline),
                      ),
                      child: ClipRRect(
                        borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                        child: SizedBox(
                          width: 120,
                          height: 120,
                          child: ClipRect(
                            child: OverflowBox(
                              alignment: Alignment.topLeft,
                              minWidth: 240,
                              maxWidth: 240,
                              minHeight: 240,
                              maxHeight: 240,
                              child: Image.asset(
                                'assets/amiya_pet_atlas.png',
                                width: 240,
                                height: 240,
                                fit: BoxFit.fill,
                                errorBuilder: (_, __, ___) => const Center(
                                  child: Icon(Icons.pets_rounded,
                                      size: 48, color: Tokens.cyan),
                                ),
                              ),
                            ),
                          ),
                        ),
                      ),
                    ),
                    const SizedBox(width: 14),
                    const Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text('待机 · 安静陪伴',
                              style: TextStyle(
                                  fontSize: 12,
                                  fontWeight: FontWeight.w700,
                                  color: Tokens.ink)),
                          SizedBox(height: 4),
                          Text('悬停 · 节点与双向网速',
                              style: TextStyle(
                                  fontSize: 12,
                                  fontWeight: FontWeight.w700,
                                  color: Tokens.cyan)),
                          SizedBox(height: 4),
                          Text('全屏 · 轻量状态条',
                              style: TextStyle(
                                  fontSize: 12,
                                  fontWeight: FontWeight.w700,
                                  color: Tokens.yellow)),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(height: 10),

              // Enable toggle
              SwitchListTile(
                contentPadding: EdgeInsets.zero,
                activeThumbColor: Tokens.yellow,
                title: const Text('启用桌面常驻',
                    style: TextStyle(
                        color: Tokens.ink,
                        fontSize: 13,
                        fontWeight: FontWeight.w600)),
                subtitle: const Text('主窗口最小化或后台运行时在桌面显示阿米娅',
                    style: TextStyle(color: Tokens.inkMuted, fontSize: 11)),
                value: _enabled,
                onChanged: (val) => setState(() => _enabled = val),
              ),

              // Scale slider
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  const Text('常态大小缩放',
                      style: TextStyle(
                          color: Tokens.ink,
                          fontSize: 12,
                          fontWeight: FontWeight.w600)),
                  Text('${(_scale * 100).round()}%',
                      style: const TextStyle(
                          color: Tokens.yellow,
                          fontSize: 11.5,
                          fontWeight: FontWeight.w700)),
                ],
              ),
              Slider(
                min: 0.6,
                max: 1.8,
                value: _scale,
                activeColor: Tokens.yellow,
                inactiveColor: Tokens.hairline,
                onChanged: _enabled
                    ? (val) => setState(() => _scale = (val * 20).round() / 20)
                    : null,
              ),

              const Divider(color: Tokens.hairline, height: 12),

              // Fullscreen compact mode toggle
              SwitchListTile(
                contentPadding: EdgeInsets.zero,
                activeThumbColor: Tokens.cyan,
                title: const Text('全屏时收为极简状态条',
                    style: TextStyle(
                        color: Tokens.ink,
                        fontSize: 13,
                        fontWeight: FontWeight.w600)),
                subtitle: const Text('其他程序全屏时自动转换为屏幕边缘轻量监视条',
                    style: TextStyle(color: Tokens.inkMuted, fontSize: 11)),
                value: _autoMini,
                onChanged:
                    _enabled ? (val) => setState(() => _autoMini = val) : null,
              ),

              // Compact width slider
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  const Text('状态条宽度',
                      style: TextStyle(
                          color: Tokens.ink,
                          fontSize: 11.5,
                          fontWeight: FontWeight.w600)),
                  Text('$_width px',
                      style: const TextStyle(
                          color: Tokens.cyan,
                          fontSize: 11,
                          fontWeight: FontWeight.w700)),
                ],
              ),
              Slider(
                min: 260.0,
                max: 560.0,
                value: _width.toDouble(),
                activeColor: Tokens.cyan,
                inactiveColor: Tokens.hairline,
                onChanged: (_enabled && _autoMini)
                    ? (val) => setState(() => _width = val.round())
                    : null,
              ),

              // Compact height slider
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  const Text('状态条高度',
                      style: TextStyle(
                          color: Tokens.ink,
                          fontSize: 11.5,
                          fontWeight: FontWeight.w600)),
                  Text('$_height px',
                      style: const TextStyle(
                          color: Tokens.cyan,
                          fontSize: 11,
                          fontWeight: FontWeight.w700)),
                ],
              ),
              Slider(
                min: 68.0,
                max: 160.0,
                value: _height.toDouble(),
                activeColor: Tokens.cyan,
                inactiveColor: Tokens.hairline,
                onChanged: (_enabled && _autoMini)
                    ? (val) => setState(() => _height = val.round())
                    : null,
              ),
              const SizedBox(height: 2),

              // Gesture hint
              const Text('单击招手 · 拖动摆放 · 双击主界面 · 右键操作',
                  style: Tokens.hint, textAlign: TextAlign.center),
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context, null),
          child: const Text('取消', style: TextStyle(color: Tokens.inkMuted)),
        ),
        FilledButton(
          style: FilledButton.styleFrom(
            backgroundColor: Tokens.yellow,
            foregroundColor: Colors.black,
            shape: RoundedRectangleBorder(
                borderRadius: BorderRadius.circular(Tokens.radiusControl)),
          ),
          onPressed: () => Navigator.pop(
              context, (_enabled, _scale, _autoMini, _width, _height)),
          child:
              const Text('应用', style: TextStyle(fontWeight: FontWeight.w700)),
        ),
      ],
    );
  }
}
