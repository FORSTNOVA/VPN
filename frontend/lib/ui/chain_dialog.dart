import 'package:flutter/material.dart';

class ChainDialog extends StatefulWidget {
  final List<String> available;
  final List<String> initialHops;
  final bool initialEnabled;

  const ChainDialog({
    super.key,
    required this.available,
    required this.initialHops,
    required this.initialEnabled,
  });

  @override
  State<ChainDialog> createState() => _ChainDialogState();
}

class _ChainDialogState extends State<ChainDialog> {
  late bool _enabled;
  late List<String> _hops;

  @override
  void initState() {
    super.initState();
    _enabled = widget.initialEnabled;
    _hops = List<String>.from(widget.initialHops);

    // If initial hops are insufficient, populate up to 2 distinct available nodes if possible
    if (_hops.length < 2) {
      for (final node in widget.available) {
        if (!_hops.contains(node)) {
          _hops.add(node);
          if (_hops.length == 2) break;
        }
      }
    }

    // A stale node must be replaced before enabling
    if (_hasStaleNodes && _enabled) {
      _enabled = false;
    }
  }

  bool _isStale(String node) => !widget.available.contains(node);

  bool get _hasStaleNodes => _hops.any(_isStale);

  bool get _hasDuplicates => _hops.toSet().length != _hops.length;

  bool get _isValidCount => _hops.length >= 2 && _hops.length <= 8;

  bool get _canSaveEnabled =>
      _isValidCount && !_hasDuplicates && !_hasStaleNodes;

  String? _getNextAvailableNode() {
    for (final node in widget.available) {
      if (!_hops.contains(node)) {
        return node;
      }
    }
    return null;
  }

  void _addHop() {
    if (_hops.length >= 8) return;
    final nextNode = _getNextAvailableNode();
    if (nextNode != null) {
      setState(() {
        _hops.add(nextNode);
      });
    } else {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('已无更多可选的独立可用节点')),
      );
    }
  }

  void _removeHop(int index) {
    if (_hops.length <= 2) return;
    setState(() {
      _hops.removeAt(index);
    });
  }

  void _onSave() {
    if (_enabled) {
      if (!_canSaveEnabled) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('链路配置无效，请修正后重试')),
        );
        return;
      }
      Navigator.of(context).pop((true, List<String>.from(_hops)));
    } else {
      Navigator.of(context).pop((false, List<String>.from(_hops)));
    }
  }

  void _onCancel() {
    Navigator.of(context).pop(null);
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final colorScheme = theme.colorScheme;
    final hasStale = _hasStaleNodes;

    return Dialog(
      insetPadding:
          const EdgeInsets.symmetric(horizontal: 16.0, vertical: 24.0),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16.0)),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420.0),
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(20.0),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // Header
              Row(
                children: [
                  Icon(Icons.alt_route_rounded, color: colorScheme.primary),
                  const SizedBox(width: 8.0),
                  const Expanded(
                    child: Text(
                      'SmartVPN 链式代理设置',
                      style: TextStyle(
                        fontSize: 18.0,
                        fontWeight: FontWeight.bold,
                      ),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16.0),

              // Enable Switch Card
              Material(
                color: colorScheme.surfaceContainerHighest.withAlpha(128),
                borderRadius: BorderRadius.circular(12.0),
                child: SwitchListTile(
                  contentPadding:
                      const EdgeInsets.symmetric(horizontal: 14, vertical: 6),
                  title: const Text(
                    '启用多跳链路',
                    style: TextStyle(fontWeight: FontWeight.w600),
                  ),
                  subtitle: Text(
                    _enabled ? '代理流量从入口进入，从最后一跳出口访问网站' : '连接时使用原有节点选择',
                    style: TextStyle(fontSize: 12.0, color: theme.hintColor),
                  ),
                  value: _enabled,
                  onChanged: (val) {
                    if (val && hasStale) {
                      ScaffoldMessenger.of(context).showSnackBar(
                        const SnackBar(content: Text('存在已失效节点，必须更换后方可启用')),
                      );
                      return;
                    }
                    if (val && !_isValidCount) {
                      ScaffoldMessenger.of(context).showSnackBar(
                        const SnackBar(content: Text('链式代理需要 2 至 8 个节点')),
                      );
                      return;
                    }
                    setState(() {
                      _enabled = val;
                    });
                  },
                ),
              ),

              if (hasStale) ...[
                const SizedBox(height: 12.0),
                Container(
                  padding: const EdgeInsets.all(10.0),
                  decoration: BoxDecoration(
                    color: colorScheme.errorContainer,
                    borderRadius: BorderRadius.circular(8.0),
                  ),
                  child: Row(
                    children: [
                      Icon(Icons.warning_amber_rounded,
                          color: colorScheme.onErrorContainer, size: 20.0),
                      const SizedBox(width: 8.0),
                      Expanded(
                        child: Text(
                          '存在已下线或失效的历史节点，必须替换为可用节点后才能启用。',
                          style: TextStyle(
                            fontSize: 12.0,
                            color: colorScheme.onErrorContainer,
                            fontWeight: FontWeight.w500,
                          ),
                        ),
                      ),
                    ],
                  ),
                ),
              ],

              const SizedBox(height: 16.0),

              // Hops list title & count info
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  const Text(
                    '节点顺序（2–8 跳）',
                    style:
                        TextStyle(fontSize: 13.0, fontWeight: FontWeight.w600),
                  ),
                  Text(
                    '当前: ${_hops.length} 节点',
                    style: TextStyle(fontSize: 12.0, color: theme.hintColor),
                  ),
                ],
              ),
              const SizedBox(height: 8.0),

              // Node List
              ...List.generate(_hops.length, (index) {
                final currentNode = _hops[index];
                final isNodeStale = _isStale(currentNode);
                final isFirst = index == 0;
                final isLast = index == _hops.length - 1;

                // Build dropdown items
                final options = <String>[];
                if (isNodeStale) {
                  options.add(currentNode);
                }
                options.addAll(widget.available);

                return Padding(
                  padding: const EdgeInsets.only(bottom: 10.0),
                  child: Row(
                    crossAxisAlignment: CrossAxisAlignment.center,
                    children: [
                      // Badge: 入口 / 出口 / 中继
                      Container(
                        width: 48.0,
                        alignment: Alignment.center,
                        padding: const EdgeInsets.symmetric(vertical: 4.0),
                        decoration: BoxDecoration(
                          color: isFirst
                              ? colorScheme.primary
                              : isLast
                                  ? colorScheme.tertiary
                                  : colorScheme.secondaryContainer,
                          borderRadius: BorderRadius.circular(6.0),
                        ),
                        child: Text(
                          isFirst ? '入口' : (isLast ? '出口' : '${index + 1}跳'),
                          style: TextStyle(
                            fontSize: 11.0,
                            fontWeight: FontWeight.bold,
                            color: isFirst
                                ? colorScheme.onPrimary
                                : isLast
                                    ? colorScheme.onTertiary
                                    : colorScheme.onSecondaryContainer,
                          ),
                        ),
                      ),
                      const SizedBox(width: 8.0),

                      // Node selector dropdown
                      Expanded(
                        child: DropdownButtonFormField<String>(
                          key: ValueKey((index, currentNode)),
                          isExpanded: true,
                          initialValue: currentNode,
                          decoration: InputDecoration(
                            isDense: true,
                            contentPadding: const EdgeInsets.symmetric(
                                horizontal: 10.0, vertical: 8.0),
                            border: OutlineInputBorder(
                              borderRadius: BorderRadius.circular(8.0),
                              borderSide: BorderSide(
                                color: isNodeStale
                                    ? colorScheme.error
                                    : colorScheme.outline,
                              ),
                            ),
                            enabledBorder: OutlineInputBorder(
                              borderRadius: BorderRadius.circular(8.0),
                              borderSide: BorderSide(
                                color: isNodeStale
                                    ? colorScheme.error
                                    : colorScheme.outline,
                              ),
                            ),
                          ),
                          items: options.map((node) {
                            final stale = _isStale(node);
                            return DropdownMenuItem<String>(
                              value: node,
                              child: Text(
                                stale ? '$node [已失效，必须更换]' : node,
                                overflow: TextOverflow.ellipsis,
                                style: TextStyle(
                                  fontSize: 13.0,
                                  color: stale
                                      ? colorScheme.error
                                      : colorScheme.onSurface,
                                  fontWeight: stale
                                      ? FontWeight.bold
                                      : FontWeight.normal,
                                ),
                              ),
                            );
                          }).toList(),
                          onChanged: (val) {
                            if (val != null) {
                              setState(() {
                                final other = _hops.indexOf(val);
                                if (other >= 0) _hops[other] = currentNode;
                                _hops[index] = val;
                              });
                            }
                          },
                        ),
                      ),

                      // Remove button
                      IconButton(
                        icon:
                            const Icon(Icons.remove_circle_outline, size: 20.0),
                        tooltip: _hops.length <= 2 ? '至少需要保留 2 个节点' : '删除该跳节点',
                        color: _hops.length <= 2
                            ? theme.disabledColor
                            : colorScheme.error,
                        onPressed:
                            _hops.length <= 2 ? null : () => _removeHop(index),
                      ),
                    ],
                  ),
                );
              }),

              // Add hop button
              OutlinedButton.icon(
                style: OutlinedButton.styleFrom(
                  shape: RoundedRectangleBorder(
                      borderRadius: BorderRadius.circular(8.0)),
                  padding: const EdgeInsets.symmetric(vertical: 8.0),
                ),
                onPressed: (_hops.length < 8 && _getNextAvailableNode() != null)
                    ? _addHop
                    : null,
                icon: const Icon(Icons.add, size: 18.0),
                label: Text(
                  _hops.length >= 8
                      ? '已达到最大 8 个节点限制'
                      : (_getNextAvailableNode() == null
                          ? '无更多可选节点'
                          : '添加中继节点'),
                  style: const TextStyle(fontSize: 13.0),
                ),
              ),

              const SizedBox(height: 20.0),

              // Action Buttons
              Row(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  TextButton(
                    onPressed: _onCancel,
                    child: const Text('取消'),
                  ),
                  const SizedBox(width: 8.0),
                  FilledButton(
                    onPressed: (_enabled && !_canSaveEnabled) ? null : _onSave,
                    child: const Text('保存'),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}
