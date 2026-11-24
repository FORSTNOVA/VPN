TextButton(
  onPressed: _kernelRunning ? null : () async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialog) => AlertDialog(
        title: const Text('删除代理链'),
        content: Text('删除「${entry.key}」？'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(dialog, false), child: const Text('取消')),
          FilledButton(onPressed: () => Navigator.pop(dialog, true), child: const Text('确认删除')),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await _request('DELETE', Uri(path: '/api/nodes/chain/profile',
          queryParameters: {'name': entry.key}).toString());
      await _loadState();
      if (context.mounted) refresh(() {});
    } catch (error) {
      if (context.mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(error.toString())));
      }
    }
  },
  child: const Text('删除代理链'),
),
