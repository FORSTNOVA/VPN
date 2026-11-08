import 'dart:collection';
import 'package:flutter/material.dart';

class InstalledGamePicker extends StatefulWidget {
  final List<Map<String, String>> apps;
  final List<String> initial;

  const InstalledGamePicker({
    super.key,
    required this.apps,
    required this.initial,
  });

  @override
  State<InstalledGamePicker> createState() => _InstalledGamePickerState();
}

class _InstalledGamePickerState extends State<InstalledGamePicker> {
  late final LinkedHashSet<String> _selectedPackages;
  final TextEditingController _searchController = TextEditingController();
  String _searchQuery = '';

  @override
  void initState() {
    super.initState();
    _selectedPackages = LinkedHashSet<String>.from(widget.initial);
    _searchController.addListener(_onSearchChanged);
  }

  void _onSearchChanged() {
    setState(() {
      _searchQuery = _searchController.text.trim().toLowerCase();
    });
  }

  @override
  void dispose() {
    _searchController.removeListener(_onSearchChanged);
    _searchController.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final filteredApps = widget.apps.where((app) {
      if (_searchQuery.isEmpty) return true;
      final label = (app['label'] ?? '').toLowerCase();
      final package = (app['package'] ?? '').toLowerCase();
      return label.contains(_searchQuery) || package.contains(_searchQuery);
    }).toList();

    final mediaQuery = MediaQuery.of(context);
    final maxHeight = mediaQuery.size.height * 0.7;

    return AlertDialog(
      title: const Text('选择已安装的游戏'),
      contentPadding: const EdgeInsets.fromLTRB(24.0, 16.0, 24.0, 0.0),
      content: SizedBox(
        width: 420,
        height: maxHeight,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            TextField(
              controller: _searchController,
              decoration: InputDecoration(
                hintText: '搜索应用名称或包名',
                prefixIcon: const Icon(Icons.search, size: 20),
                suffixIcon: _searchQuery.isNotEmpty
                    ? IconButton(
                        icon: const Icon(Icons.clear, size: 20),
                        onPressed: () => _searchController.clear(),
                      )
                    : null,
                isDense: true,
                contentPadding: const EdgeInsets.symmetric(
                  horizontal: 12.0,
                  vertical: 10.0,
                ),
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(8.0),
                ),
              ),
            ),
            const SizedBox(height: 12.0),
            Flexible(
              child: filteredApps.isEmpty
                  ? const Center(
                      child: Padding(
                        padding: EdgeInsets.all(24.0),
                        child: Text(
                          '未找到匹配的游戏',
                          style: TextStyle(color: Colors.grey),
                        ),
                      ),
                    )
                  : ListView.builder(
                      shrinkWrap: true,
                      itemCount: filteredApps.length,
                      itemBuilder: (context, index) {
                        final app = filteredApps[index];
                        final package = app['package'] ?? '';
                        final label = app['label'] ?? package;
                        final isChecked = _selectedPackages.contains(package);

                        return CheckboxListTile(
                          dense: true,
                          value: isChecked,
                          title: Text(
                            label,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                          ),
                          subtitle: Text(
                            package,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(fontSize: 11.0),
                          ),
                          controlAffinity: ListTileControlAffinity.leading,
                          contentPadding: EdgeInsets.zero,
                          onChanged: (bool? checked) {
                            setState(() {
                              if (checked == true) {
                                _selectedPackages.add(package);
                              } else {
                                _selectedPackages.remove(package);
                              }
                            });
                          },
                        );
                      },
                    ),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(null),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: () =>
              Navigator.of(context).pop(_selectedPackages.toList()),
          child: Text('确定 (${_selectedPackages.length})'),
        ),
      ],
    );
  }
}
