import 'package:flutter/material.dart';

class StartupTile extends StatelessWidget {
  const StartupTile({
    super.key,
    required this.android,
    required this.enabled,
    required this.onChanged,
    required this.openVpnSettings,
  });

  final bool android;
  final bool? enabled;
  final ValueChanged<bool>? onChanged;
  final VoidCallback? openVpnSettings;

  @override
  Widget build(BuildContext context) {
    if (android) {
      return ListTile(
        contentPadding: EdgeInsets.zero,
        title: const Text('开机自动连接'),
        subtitle: const Text('请在系统 VPN 设置中为 SmartVPN 打开「始终开启 VPN」'),
        trailing: const Icon(Icons.chevron_right),
        onTap: openVpnSettings,
      );
    }

    return SwitchListTile.adaptive(
      contentPadding: EdgeInsets.zero,
      title: const Text('登录时自动连接'),
      subtitle: const Text('启动 SmartVPN 并优先连接上次使用的节点'),
      value: enabled ?? false,
      onChanged: enabled == null ? null : onChanged,
    );
  }
}
