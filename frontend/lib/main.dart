import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'models.dart';
import 'tokens.dart';

export 'models.dart';

part 'ui/checks_page.dart';
part 'ui/connect_page.dart';
part 'ui/nodes_page.dart';
part 'ui/region_page.dart';
part 'ui/subscription_page.dart';
part 'ui/traffic_page.dart';
part 'ui/widgets.dart';

void main() => runApp(const SmartVpnApp());

/// The sub-pages of the rail, in the order they are offered.
enum _AppPage { connect, traffic, nodes, region, checks, subscription }

extension on _AppPage {
  String get label => switch (this) {
        _AppPage.connect => '连接',
        _AppPage.traffic => '流量',
        _AppPage.nodes => '节点',
        _AppPage.region => '地区与健康',
        _AppPage.checks => '检测',
        _AppPage.subscription => '订阅与内核',
      };

  String get englishCode => switch (this) {
        _AppPage.connect => 'CONNECT_HUB',
        _AppPage.traffic => 'TRAFFIC_MONITOR',
        _AppPage.nodes => 'NODE_MATRIX',
        _AppPage.region => 'REGION_HEALTH',
        _AppPage.checks => 'DIAGNOSTIC_SUITE',
        _AppPage.subscription => 'KERNEL_SUITE',
      };

  IconData get icon => switch (this) {
        _AppPage.connect => Icons.power_settings_new_rounded,
        _AppPage.traffic => Icons.monitor_heart_outlined,
        _AppPage.nodes => Icons.dns_rounded,
        _AppPage.region => Icons.public_rounded,
        _AppPage.checks => Icons.fact_check_outlined,
        _AppPage.subscription => Icons.tune_rounded,
      };
}

class SmartVpnApp extends StatelessWidget {
  const SmartVpnApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'SmartVPN',
      debugShowCheckedModeBanner: false,
      theme: Tokens.theme(),
      home: const HomePage(),
    );
  }
}

class HomePage extends StatefulWidget {
  const HomePage({super.key});

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> with WidgetsBindingObserver {
  /// The runner owns the window and the tray icon, so it is the one that knows a
  /// close was requested; this channel is how it asks and how it is answered. It
  /// only exists in the Windows runner, so every call has to tolerate its
  /// absence (tests, other platforms).
  static const MethodChannel _lifecycle = MethodChannel('smartvpn/lifecycle');

  final _subscription = TextEditingController();
  final _mihomo = TextEditingController();
  final _wintun = TextEditingController();
  final _nodeSearch = TextEditingController();
  int? _port;
  String? _token;
  bool _connected = false;
  bool _proxyEnabled = false;
  Timer? _stateTimer;
  int _mixedPort = 0;
  bool _busy = false;
  bool _hasFetchedNodes = false;
  String _savedSubscriptionUrl = '';
  String? _error;
  int? _latency;
  List<ProxyNode> _nodes = const [];
  String? _selectedNode;
  String _activeNode = '';
  DiagnoseReport? _diagnose;
  bool _diagnosing = false;
  List<RegionInfo> _regionNodes = const [];
  RegionJob _regionJob = const RegionJob(
      running: false,
      total: 0,
      done: 0,
      verified: 0,
      skipped: 0,
      failed: 0,
      lastError: '');
  HealthSummary? _health;
  String _lockedRegion = '';
  String _activeRegion = '';
  bool _blocked = false;
  String _blockReason = '';
  List<String> _regionPool = const [];
  Map<String, NodeHealth> _nodeHealth = const {};
  Map<String, RegionInfo> _regionByName = const {};
  String _connectionMode = 'system-proxy';
  bool _elevated = false;
  bool _tunAvailable = false;
  String _tunReason = '';
  bool _tunActive = false;
  ChinaDirect _chinaDirect = const ChinaDirect.empty();
  String _wintunPath = '';
  TunStatus? _tun;
  bool _desktopPetEnabled = true;
  double _desktopPetScale = 1.0;
  bool _desktopPetAutoMini = true;
  int _desktopPetWidth = 360;
  int _desktopPetHeight = 82;

  _AppPage _page = _AppPage.connect;
  NodeGrouping _nodeGrouping = NodeGrouping.region;
  String _nodeQuery = '';
  Map<String, int> _nodeLatency = const {};
  bool _measuringNodes = false;
  List<TunCheck> _tunChecks = const [];
  bool _tunChecksActive = false;
  SpeedTestResult? _speed;
  bool _speedRunning = false;
  int _speedMegaBytes = 30;
  PathChecks _pathChecks = const PathChecks.empty();
  PathStatus _pathStatus = const PathStatus.empty();
  int _shownPathAlertId = 0;
  bool _pathAlertOpen = false;
  PathLimits _pathLimits = const PathLimits.fallback();
  final _pathInterval = TextEditingController();
  final _pathLabel = TextEditingController();
  final _pathURL = TextEditingController();
  Timer? _pathTimer;
  bool _pathSaving = false;
  TrafficSnapshot _traffic = const TrafficSnapshot.idle();
  Timer? _trafficTimer;
  bool _trafficPaused = false;
  bool _trafficLoading = false;
  DateTime? _desktopPetTrafficAt;
  int? _desktopPetUploadTotal;
  int? _desktopPetDownloadTotal;
  int _desktopPetUploadRate = 0;
  int _desktopPetDownloadRate = 0;
  List<SavedSubscription> _savedSubscriptions = const [];
  int _autoMergeHours = 0;
  bool _subscriptionsFromCache = false;
  bool _kernelRunning = false;
  String _homePath = '';
  bool _portable = false;
  String _mihomoResolved = '';

  int get _poolSize => _health?.poolSize ?? 0;
  HealthParams get _healthParams =>
      _health?.params ?? HealthParams.fromJson(const {});

  /// Whether traffic is actually being protected right now: the system proxy in
  /// one mode, the TUN adapter in the other.
  /// Whether traffic is actually being carried by the mode in use: the tunnel
  /// for TUN and for Android's VPN, the machine's proxy setting for the system
  /// proxy. A connection that is up but carrying nothing is the state this
  /// exists to tell apart, and on Android there is no setting to be out of step
  /// — the tunnel is the only thing that can be.
  bool get _protectedNow =>
      _connectionMode == 'system-proxy' ? _proxyEnabled : _tunActive;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _lifecycle.setMethodCallHandler(_handleLifecycleCall);
    unawaited(_loadDesktopPetSettings());
    unawaited(_startService());
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.detached && _port != null) {
      unawaited(_request('POST', '/api/shutdown')
          .catchError((_) => <String, dynamic>{}));
    }
  }

  /// The window's close button and the tray menu arrive here: the runner owns
  /// both, but only this side can ask the user or stop the local service.
  Future<void> _handleLifecycleCall(MethodCall call) async {
    if (call.method == 'closeRequested') {
      final arguments = call.arguments as Map<Object?, Object?>? ?? const {};
      await _confirmClose(trayReady: arguments['trayReady'] as bool? ?? false);
      return;
    }
    if (call.method == 'quitRequested') {
      await _quitAndClose();
      return;
    }
    if (call.method == 'toggleConnection') {
      await _toggleConnection();
      await _pushTrayStatus();
      return;
    }
    if (call.method == 'toggleMode') {
      if (!_busy && !_connected) {
        final newMode = _connectionMode == 'tun' ? 'system-proxy' : 'tun';
        await _setConnectionMode(newMode);
        await _pushDesktopPetStatus();
      }
      return;
    }
    if (call.method == 'measureLatency') {
      await _measureLatency();
      await _pushDesktopPetStatus();
      return;
    }
    if (call.method == 'selectNode') {
      final arguments = call.arguments as Map<Object?, Object?>? ?? const {};
      final name = arguments['name'] as String?;
      if (name != null && name.isNotEmpty) {
        await _selectNode(name);
        await _pushDesktopPetStatus();
      }
      return;
    }
    if (call.method == 'openPetSettings') {
      await _showDesktopPetSettings();
      return;
    }
    if (call.method == 'petToggled') {
      final arguments = call.arguments as Map<Object?, Object?>? ?? const {};
      final enabled = arguments['enabled'] as bool? ?? false;
      if (mounted) setState(() => _desktopPetEnabled = enabled);
      return;
    }
  }

  /// Closing is a choice rather than an exit: hiding keeps the tunnel up and the
  /// window one click away, while quitting has to stop the local service so the
  /// Windows proxy settings are handed back to the user.
  Future<void> _confirmClose({required bool trayReady}) async {
    if (!mounted) return;
    final choice = await showDialog<CloseChoice>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('关闭窗口？'),
        content: Text(trayReady
            ? '最小化到托盘会保持当前连接，左键托盘图标即可恢复窗口。'
                '退出会先断开连接、停止本地服务，并把系统代理设置恢复原样。'
            : '托盘图标不可用，所以现在不能最小化到托盘 —— 那会让你找不到窗口。'
                '退出会先断开连接、停止本地服务，并把系统代理设置恢复原样。'),
        actions: [
          for (final option in closeChoices(trayReady: trayReady))
            TextButton(
              onPressed: () => Navigator.of(context).pop(option),
              child: Text(closeChoiceLabel(option)),
            ),
        ],
      ),
    );
    // A dialog dismissed without a choice is a cancel like any other.
    final action = closeChoiceAction(choice ?? CloseChoice.cancel);
    if (action == 'quit') {
      await _quitAndClose();
      return;
    }
    await _invokeLifecycle('closeChoice', {'action': action});
  }

  /// Stops the service first — that is what disconnects and restores the proxy —
  /// and never lets a failing request leave the window stuck on screen.
  Future<void> _quitAndClose() async {
    try {
      await _request('POST', '/api/shutdown');
    } catch (_) {
      // The service is already gone or unreachable; the window still has to go.
    }
    await _invokeLifecycle('closeChoice', {'action': 'quit'});
  }

  /// The lifecycle channel only exists in the Windows runner.
  Future<void> _invokeLifecycle(String method,
      [Map<String, Object?>? arguments]) async {
    try {
      await _lifecycle.invokeMethod<void>(method, arguments);
    } on MissingPluginException {
      // Running somewhere without a runner to talk to.
    }
  }

  Future<void> _pushTrayStatus() async {
    await _invokeLifecycle('setStatus', {
      'tooltip': trayTooltip(connected: _connected, node: _activeNode),
      'connected': _connected,
    });
    await _pushDesktopPetStatus();
  }

  Future<void> _loadDesktopPetSettings() async {
    if (!Platform.isWindows) return;
    try {
      final settings = Map<Object?, Object?>.from(await _lifecycle
              .invokeMethod<Map<Object?, Object?>>('getPetSettings') ??
          const <Object?, Object?>{});
      if (!mounted) return;
      setState(() {
        _desktopPetEnabled = settings['enabled'] as bool? ?? true;
        final scaleVal = settings['scale'];
        if (scaleVal is num) {
          _desktopPetScale = scaleVal.toDouble().clamp(0.6, 1.8);
        }
        _desktopPetAutoMini = settings['autoMiniOnFullscreen'] as bool? ?? true;
        _desktopPetWidth = settings['width'] as int? ?? 360;
        _desktopPetHeight = settings['height'] as int? ?? 82;
      });
      _syncTrafficPoll();
      await _pushDesktopPetStatus();
    } on MissingPluginException {
      // Tests and non-runner builds do not have the Windows pet window.
    } on PlatformException {
      // Keep the defaults if the native window is unavailable.
    }
  }

  Future<void> _configureDesktopPet({
    bool? enabled,
    double? scale,
    bool? autoMiniOnFullscreen,
    int? width,
    int? height,
  }) async {
    setState(() {
      if (enabled != null) _desktopPetEnabled = enabled;
      if (scale != null) _desktopPetScale = scale;
      if (autoMiniOnFullscreen != null) {
        _desktopPetAutoMini = autoMiniOnFullscreen;
      }
      if (width != null) _desktopPetWidth = width;
      if (height != null) _desktopPetHeight = height;
    });
    await _invokeLifecycle('configurePet', {
      'enabled': _desktopPetEnabled,
      'scale': _desktopPetScale,
      'autoMiniOnFullscreen': _desktopPetAutoMini,
      'width': _desktopPetWidth,
      'height': _desktopPetHeight,
    });
    _syncTrafficPoll();
    await _pushDesktopPetStatus();
  }

  Future<void> _pushDesktopPetStatus() async {
    if (!Platform.isWindows || !_desktopPetEnabled) return;
    final status = _blocked
        ? '流量已阻断'
        : (_protectedNow ? '已连接' : (_connected ? '代理未启用' : '未连接'));
    await _invokeLifecycle('setPetStatus', {
      'status': status,
      'connected': _connected,
      'node': _activeNode.isNotEmpty ? _activeNode : (_selectedNode ?? ''),
      'mode': _connectionMode,
      'nodes': _nodes.map((n) => n.name).toList(),
      'downloadRate': _desktopPetDownloadRate,
      'uploadRate': _desktopPetUploadRate,
    });
  }

  /// The pet has its own sampling baseline so another traffic-page request
  /// cannot reset the shared API rate and leave the floating widget at zero.
  void _sampleDesktopPetTraffic(TrafficSnapshot snapshot) {
    if (!snapshot.available) {
      _desktopPetTrafficAt = null;
      _desktopPetUploadTotal = null;
      _desktopPetDownloadTotal = null;
      _desktopPetUploadRate = 0;
      _desktopPetDownloadRate = 0;
      return;
    }

    final now = DateTime.now();
    final previousAt = _desktopPetTrafficAt;
    final previousUpload = _desktopPetUploadTotal;
    final previousDownload = _desktopPetDownloadTotal;
    final elapsed = previousAt == null
        ? 0.0
        : now.difference(previousAt).inMicroseconds / 1000000.0;
    if (previousAt != null &&
        previousUpload != null &&
        previousDownload != null &&
        elapsed >= 0.25 &&
        snapshot.uploadTotal >= previousUpload &&
        snapshot.downloadTotal >= previousDownload) {
      _desktopPetUploadRate =
          ((snapshot.uploadTotal - previousUpload) / elapsed).round();
      _desktopPetDownloadRate =
          ((snapshot.downloadTotal - previousDownload) / elapsed).round();
    } else {
      // The first reading and a restarted kernel establish a fresh baseline.
      _desktopPetUploadRate = 0;
      _desktopPetDownloadRate = 0;
    }
    _desktopPetTrafficAt = now;
    _desktopPetUploadTotal = snapshot.uploadTotal;
    _desktopPetDownloadTotal = snapshot.downloadTotal;
  }

  Future<void> _showDesktopPetSettings() async {
    if (!Platform.isWindows || !mounted) return;
    var enabled = _desktopPetEnabled;
    var scale = _desktopPetScale;
    var autoMini = _desktopPetAutoMini;
    var width = _desktopPetWidth;
    var height = _desktopPetHeight;
    final result = await showDialog<(bool, double, bool, int, int)>(
      context: context,
      builder: (context) => StatefulBuilder(
        builder: (context, setDialogState) => AlertDialog(
          title: const Row(
            children: [
              Icon(Icons.pets_rounded, size: 20, color: Tokens.cyan),
              SizedBox(width: 8),
              Text('阿米娅桌宠 · 罗德岛指挥终端'),
            ],
          ),
          content: SizedBox(
            width: 440,
            child: SingleChildScrollView(
              child: Column(mainAxisSize: MainAxisSize.min, children: [
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: Tokens.surfaceElevated,
                    borderRadius: BorderRadius.circular(Tokens.radiusControl),
                    border: Border.all(color: Tokens.hairline),
                  ),
                  child: Row(children: [
                    ClipRRect(
                      borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                      child: Image.asset(
                        'assets/amiya_chibi.png',
                        height: 96,
                        fit: BoxFit.contain,
                        errorBuilder: (_, __, ___) => const Icon(
                          Icons.pets_rounded,
                          size: 64,
                          color: Tokens.cyan,
                        ),
                      ),
                    ),
                    const SizedBox(width: 14),
                    const Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text('阿米娅 (Amiya)',
                              style: TextStyle(
                                  fontWeight: FontWeight.w700,
                                  fontSize: 14,
                                  color: Tokens.cyan)),
                          SizedBox(height: 4),
                          Text(
                            '“博士，阿米娅随时为您守护网络连接！在桌面上右键我可以快速开关、测速、切换节点及模式噢~”',
                            style: TextStyle(fontSize: 12, height: 1.4),
                          ),
                        ],
                      ),
                    ),
                  ]),
                ),
                const SizedBox(height: 14),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('在电脑桌面显示'),
                  subtitle: const Text('即使最小化或关闭主窗口，阿米娅仍在桌面常驻陪伴'),
                  value: enabled,
                  onChanged: (value) => setDialogState(() => enabled = value),
                ),
                const SizedBox(height: 8),
                Row(children: [
                  const Expanded(
                    child: Text('桌宠显示大小 (缩放比例)',
                        style: TextStyle(fontWeight: FontWeight.w600)),
                  ),
                  Text('${(scale * 100).round()}%', style: Tokens.number),
                ]),
                Slider(
                  min: 0.6,
                  max: 1.8,
                  divisions: 12,
                  value: scale,
                  label: '${(scale * 100).round()}%',
                  onChanged: enabled
                      ? (value) {
                          final rounded = (value * 20).round() / 20;
                          setDialogState(() => scale = rounded);
                          _configureDesktopPet(
                            enabled: enabled,
                            scale: rounded,
                            autoMiniOnFullscreen: autoMini,
                            width: width,
                            height: height,
                          );
                        }
                      : null,
                ),
                const SizedBox(height: 8),
                const Divider(),
                const SizedBox(height: 6),
                const Align(
                  alignment: Alignment.centerLeft,
                  child: Text(
                    '全屏极简小窗模式',
                    style: TextStyle(
                      fontSize: 12,
                      fontWeight: FontWeight.w700,
                      color: Tokens.cyan,
                      letterSpacing: 0.5,
                    ),
                  ),
                ),
                const SizedBox(height: 4),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('全屏时自动收为小窗口'),
                  subtitle: const Text('检测到其他程序全屏时，自动缩为右上角极简状态条，仅显示连接状态与实时网速'),
                  value: autoMini,
                  onChanged: enabled
                      ? (value) => setDialogState(() => autoMini = value)
                      : null,
                ),
                const SizedBox(height: 8),
                Row(children: [
                  const Expanded(child: Text('小窗宽度')),
                  Text('$width px', style: Tokens.number),
                ]),
                Slider(
                  min: 260,
                  max: 560,
                  divisions: 30,
                  value: width.toDouble(),
                  label: '$width px',
                  onChanged: (enabled && autoMini)
                      ? (value) {
                          final v = value.round();
                          setDialogState(() => width = v);
                          _configureDesktopPet(
                              enabled: enabled,
                              scale: scale,
                              autoMiniOnFullscreen: autoMini,
                              width: v,
                              height: height);
                        }
                      : null,
                ),
                Row(children: [
                  const Expanded(child: Text('小窗高度')),
                  Text('$height px', style: Tokens.number),
                ]),
                Slider(
                  min: 68,
                  max: 160,
                  divisions: 23,
                  value: height.toDouble(),
                  label: '$height px',
                  onChanged: (enabled && autoMini)
                      ? (value) {
                          final v = value.round();
                          setDialogState(() => height = v);
                          _configureDesktopPet(
                              enabled: enabled,
                              scale: scale,
                              autoMiniOnFullscreen: autoMini,
                              width: width,
                              height: v);
                        }
                      : null,
                ),
                const SizedBox(height: 6),
                const Align(
                  alignment: Alignment.centerLeft,
                  child: Text(
                    '提示：桌宠平时可通过鼠标左键按住拖拽调整桌面摆放位置；右键可展开完整控制菜单。',
                    style: Tokens.hint,
                  ),
                ),
              ]),
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context),
              child: const Text('取消'),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(
                  context, (enabled, scale, autoMini, width, height)),
              child: const Text('确定'),
            ),
          ],
        ),
      ),
    );
    if (result != null && mounted) {
      await _configureDesktopPet(
        enabled: result.$1,
        scale: result.$2,
        autoMiniOnFullscreen: result.$3,
        width: result.$4,
        height: result.$5,
      );
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _stateTimer?.cancel();
    _trafficTimer?.cancel();
    _pathTimer?.cancel();
    _pathInterval.dispose();
    _pathLabel.dispose();
    _pathURL.dispose();
    _subscription.dispose();
    _mihomo.dispose();
    _wintun.dispose();
    _nodeSearch.dispose();
    super.dispose();
  }

  // -------------------------------------------------------------------------
  // Service client
  // -------------------------------------------------------------------------

  /// The channel to the platform half of this app. On Windows the runner uses it
  /// for the tray and for closing; on Android it carries the two things the
  /// window cannot find out for itself — the address of the local service, and
  /// whether the system granted a tunnel.
  static const MethodChannel _host = MethodChannel('smartvpn/host');

  Future<void> _startService() async {
    try {
      if (Platform.isAndroid) {
        await _startAndroidService();
        return;
      }
      final exe = File(Platform.resolvedExecutable).parent;
      final updatedService = File(
          '${exe.path}${Platform.pathSeparator}smartvpn-service-current.exe');
      final serviceExe = await updatedService.exists()
          ? updatedService
          : File('${exe.path}${Platform.pathSeparator}smartvpn-service.exe');
      if (!await serviceExe.exists()) {
        throw Exception('找不到本地服务：${serviceExe.path}');
      }
      // The service owns its own lifetime and may be shared with another window,
      // so this handle is only used to read the bootstrap line.
      final process =
          await Process.start(serviceExe.path, const [], runInShell: false);
      // A service that has nothing to do fails on the spot and closes the pipe,
      // which the stream reports at once. The timeout only covers a launch that
      // is waiting: for another launch to finish starting, or for the service it
      // outranks to disconnect and stop its kernel before the new mode can take
      // over.
      final line = await process.stdout
          .transform(utf8.decoder)
          .transform(const LineSplitter())
          .first
          .timeout(const Duration(seconds: 30));
      await _adoptBootstrap(line);
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  /// On Android there is no second program to start. The kernel, the scheduling
  /// and the local API are one library inside this process, and what the window
  /// asks the platform for is the address that library answers on. The answer is
  /// the same bootstrap record the Windows service prints, so everything that
  /// reads it is the same code.
  Future<void> _startAndroidService() async {
    final answer = await _host.invokeMethod<String>('service');
    if (answer == null || answer.isEmpty) {
      throw Exception('本地服务没有响应');
    }
    await _adoptBootstrap(answer);
  }

  Future<void> _adoptBootstrap(String line) async {
    final bootstrap = jsonDecode(line) as Map<String, dynamic>;
    // A launch that refused to serve says so in place of a port.
    final refusal = bootstrap['error'] as String?;
    if (refusal != null && refusal.isNotEmpty) {
      throw Exception(refusal);
    }
    _port = bootstrap['port'] as int;
    _token = bootstrap['token'] as String;
    await _loadState();
    _stateTimer = Timer.periodic(
        const Duration(seconds: 15), (_) => unawaited(_pollConnectionState()));
  }

  /// Asks the platform for the tunnel this app's mode needs, and refuses the
  /// connection if it is not granted.
  ///
  /// On Android that is the user's authorisation of a VpnService, which only the
  /// activity can ask for, and a descriptor only the service's own process can
  /// hold. On Windows there is nothing to ask: the kernel creates its own
  /// adapter, with the administrator token the service already holds.
  Future<void> _openTunnel() async {
    if (!Platform.isAndroid) return;
    final answer = await _host.invokeMethod<String>('connect');
    Map<String, dynamic>? result;
    if (answer != null && answer.isNotEmpty) {
      result = jsonDecode(answer) as Map<String, dynamic>;
    }
    if (result == null || result['ok'] != true) {
      final message = (result?['message'] as String?) ?? '';
      throw Exception(message.isEmpty ? '无法建立 VPN 隧道' : message);
    }
  }

  /// Gives the tunnel back. The connection is already down by the time this
  /// runs, so a tunnel torn down here is one that carries nothing.
  Future<void> _closeTunnel() async {
    if (!Platform.isAndroid) return;
    await _host.invokeMethod<void>('disconnect');
  }

  Future<Map<String, dynamic>> _request(
    String method,
    String route, {
    Map<String, dynamic>? body,
  }) async {
    final port = _port;
    final token = _token;
    if (port == null || token == null) throw Exception('本地服务尚未启动');
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 5);
    try {
      final request = await client.openUrl(
          method, Uri.parse('http://127.0.0.1:$port$route'));
      request.headers.set(HttpHeaders.authorizationHeader, 'Bearer $token');
      if (body != null) {
        request.headers.contentType = ContentType.json;
        request.write(jsonEncode(body));
      }
      final response = await request.close();
      final text = await utf8.decoder.bind(response).join();
      if (response.statusCode < 200 || response.statusCode >= 300) {
        throw Exception(text.trim().isEmpty ? '本地服务请求失败' : text.trim());
      }
      return text.isEmpty
          ? <String, dynamic>{}
          : jsonDecode(text) as Map<String, dynamic>;
    } finally {
      client.close(force: true);
    }
  }

  Future<void> _loadState() async {
    final value = await _request('GET', '/api/state');
    if (!mounted) return;
    setState(() {
      final sub = value['subscriptionUrl'] as String? ?? '';
      _savedSubscriptionUrl = sub;
      if (sub != '__merged__') {
        _subscription.text = sub;
      } else {
        _subscription.clear();
      }
      _mihomo.text = value['mihomoPath'] as String? ?? _mihomo.text;
      _connected = value['connected'] as bool? ?? false;
      _proxyEnabled = value['proxyEnabled'] as bool? ?? false;
      _mixedPort = value['mixedPort'] as int? ?? 0;
      _selectedNode = value['selectedNode'] as String?;
      _activeNode = value['activeNode'] as String? ?? '';
      _applyRegionState(value);
      _applyTunState(value);
      _applyChinaState(value);
      _wintun.text = _wintunPath;
      // Where the service keeps things, and the kernel it will actually run —
      // which differs from the field above whenever the kernel came with a
      // package rather than being chosen by hand.
      _homePath = value['home'] as String? ?? '';
      _portable = value['portable'] as bool? ?? false;
      _mihomoResolved = value['mihomoResolved'] as String? ?? '';
    });
    await _loadNodes();
    await _loadRegions();
    await _loadTun();
    await _pushTrayStatus();
    await _loadSubscriptions();
    await _loadPathChecks();
    _syncPathPoll();
    _syncTrafficPoll();
  }

  void _applyRegionState(Map<String, dynamic> value) {
    _lockedRegion = value['lockedRegion'] as String? ?? '';
    _activeRegion = value['activeRegion'] as String? ?? '';
    _blocked = value['blocked'] as bool? ?? false;
    _blockReason = value['blockReason'] as String? ?? '';
    _regionJob = RegionJob.fromJson(
        value['regionJob'] as Map<String, dynamic>? ?? const {});
    _health = HealthSummary.fromJson(
        value['health'] as Map<String, dynamic>? ?? const {});
  }

  void _applyTunState(Map<String, dynamic> value) {
    _connectionMode = value['connectionMode'] as String? ?? 'system-proxy';
    _elevated = value['elevated'] as bool? ?? false;
    _tunAvailable = value['tunAvailable'] as bool? ?? false;
    _tunReason = value['tunReason'] as String? ?? '';
    _tunActive = value['tunActive'] as bool? ?? false;
    _wintunPath = value['wintunPath'] as String? ?? '';
  }

  void _applyChinaState(Map<String, dynamic> value) {
    _chinaDirect = ChinaDirect.fromJson(
        value['chinaDirect'] as Map<String, dynamic>? ?? const {});
  }

  /// Asks the service for the list again. The download happens there, so this
  /// follows it rather than waiting for the next state poll, which is fifteen
  /// seconds away.
  Future<void> _refreshChinaDirect() async {
    try {
      await _request('POST', '/api/direct/refresh');
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
      return;
    }
    if (!mounted) return;
    setState(() => _chinaDirect = _chinaDirect.asRefreshing());
    for (var attempt = 0; attempt < 20; attempt++) {
      await Future<void>.delayed(const Duration(seconds: 1));
      if (!mounted) return;
      try {
        final value = await _request('GET', '/api/state');
        if (!mounted) return;
        setState(() => _applyChinaState(value));
        if (!_chinaDirect.refreshing) return;
      } catch (_) {
        // The service is gone or busy; the state poll will settle it.
        return;
      }
    }
  }

  Future<void> _pollConnectionState() async {
    if (_busy || _port == null) return;
    try {
      final wasRunning = _regionJob.running;
      final value = await _request('GET', '/api/state');
      if (!mounted || _busy) return;
      setState(() {
        _connected = value['connected'] as bool? ?? false;
        _proxyEnabled = value['proxyEnabled'] as bool? ?? false;
        _activeNode = value['activeNode'] as String? ?? '';
        _applyRegionState(value);
        _applyTunState(value);
        _applyChinaState(value);
      });
      // The per-node region table only lives on /api/regions, so refresh it
      // while a check runs and once more when it finishes.
      if (_regionJob.running || wasRunning) {
        await _loadRegions();
      }
      // The tray is the only thing visible while the window is hidden, so its
      // tooltip follows the state even when nothing else changes on screen.
      await _pushTrayStatus();
    } catch (_) {
      // Keep the current view until the local service becomes reachable again.
    }
  }

  void _goTo(_AppPage page) {
    _refresh(() => _page = page);
    unawaited(_loadTun());
    _syncTrafficPoll();
    if (page == _AppPage.subscription) {
      unawaited(_loadSubscriptions());
    }
    if (page == _AppPage.checks) {
      unawaited(_loadPathChecks());
    }
    // The path status is only interesting while its page is open, and the monitor
    // may have been switched on from there.
    _syncPathPoll();
  }

  /// Traffic only changes while the kernel is carrying something, so it is
  /// sampled while the page that shows it is open and stopped everywhere else.
  void _syncTrafficPoll() {
    final petNeedsRates = Platform.isWindows && _desktopPetEnabled;
    final wanted =
        ((_page == _AppPage.traffic && !_trafficPaused) || petNeedsRates) &&
            _port != null;
    if (!wanted) {
      _trafficTimer?.cancel();
      _trafficTimer = null;
      return;
    }
    _trafficTimer ??= Timer.periodic(
        const Duration(seconds: 2), (_) => unawaited(_loadTraffic()));
    unawaited(_loadTraffic());
  }

  /// A poll that fails leaves a reason on the page instead of an error banner:
  /// this runs every two seconds, and a service that has gone away is already
  /// reported by the connection page.
  Future<void> _loadTraffic() async {
    if (_port == null || _trafficLoading) return;
    _trafficLoading = true;
    try {
      final value = await _request('GET', '/api/traffic');
      if (!mounted) return;
      final snapshot = TrafficSnapshot.fromJson(value);
      _sampleDesktopPetTraffic(snapshot);
      setState(() => _traffic = snapshot);
      await _pushDesktopPetStatus();
    } catch (_) {
      if (mounted) {
        _sampleDesktopPetTraffic(const TrafficSnapshot.unavailable(''));
        setState(() =>
            _traffic = const TrafficSnapshot.unavailable('本地服务不可达，监控已暂停'));
      }
    } finally {
      _trafficLoading = false;
    }
  }

  /// The sub-page extensions live in part files, which cannot reach the
  /// protected setState, so they go through here.
  void _refresh(VoidCallback mutate) => setState(mutate);

  // -------------------------------------------------------------------------
  // Actions
  // -------------------------------------------------------------------------

  Future<void> _reapplyProxy() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('POST', '/api/proxy/reapply');
      await _loadState();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  bool get _isMergedSubscription => _savedSubscriptionUrl == '__merged__';

  Future<void> _saveSettings() async {
    final newSubscriptionUrl = _subscription.text.trim();
    final effectiveUrl = (newSubscriptionUrl.isEmpty && _isMergedSubscription)
        ? '__merged__'
        : newSubscriptionUrl;
    await _request('PUT', '/api/settings', body: {
      'subscriptionUrl': effectiveUrl,
      'mihomoPath': _mihomo.text.trim(),
      'connectionMode': _connectionMode,
      'wintunPath': _wintun.text.trim(),
    });
    if (effectiveUrl != _savedSubscriptionUrl) {
      _savedSubscriptionUrl = effectiveUrl;
      _nodes = const [];
      _selectedNode = null;
      _hasFetchedNodes = false;
    }
    // The service registers a URL typed here in the saved list.
    await _loadSubscriptions();
  }

  Future<void> _toggleConnection() async {
    setState(() {
      _busy = true;
      _error = null;
      _latency = null;
      _diagnose = null;
      _speed = null;
    });
    try {
      if (_connected) {
        await _request('POST', '/api/disconnect');
        await _closeTunnel();
        await _loadState();
      } else {
        await _saveSettings();
        // The tunnel comes up before the service is asked to connect, because
        // the kernel is handed the tunnel's descriptor as it starts.
        await _openTunnel();
        final Map<String, dynamic> result;
        try {
          result = await _request('POST', '/api/connect');
        } catch (e) {
          // A tunnel with nothing behind it is worse than no tunnel: every
          // application's traffic is routed into it and dropped, so the device
          // loses its network while the window shows a connection. A connection
          // that did not come up takes the tunnel down with it.
          await _closeTunnel();
          rethrow;
        }
        await _loadState();
        if (mounted && result['autoSwitched'] == true) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(
                content: Text('原节点不可达，已自动切换到可访问 YouTube 和 ChatGPT 的节点')),
          );
        }
      }
    } catch (e) {
      setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  void _applyNodes(Map<String, dynamic> value) {
    final rawNodes = value['nodes'] as List<dynamic>? ?? const [];
    final nodes = rawNodes
        .whereType<Map<String, dynamic>>()
        .map(ProxyNode.fromJson)
        .where((node) => node.name.isNotEmpty)
        .toList(growable: false);
    String? selectedNode = _selectedNode;
    for (final node in nodes) {
      if (node.selected) {
        selectedNode = node.name;
        break;
      }
    }
    if (!nodes.any((node) => node.name == selectedNode)) selectedNode = null;
    setState(() {
      _nodes = nodes;
      _selectedNode = selectedNode;
    });
  }

  Future<void> _loadNodes() async {
    final value = await _request('GET', '/api/nodes');
    if (!mounted) return;
    _applyNodes(value);
    final rawNodes = value['nodes'] as List<dynamic>? ?? const [];
    setState(() => _hasFetchedNodes = rawNodes.isNotEmpty);
  }

  Future<void> _refreshNodes() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/nodes/refresh');
      if (mounted) _applyNodes(value);
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _fetchNodes() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _saveSettings();
      final value = await _request('POST', '/api/nodes/fetch');
      if (mounted) {
        _applyNodes(value);
        setState(() => _hasFetchedNodes = true);
        final appliedFormat = value['appliedFormat'] as String? ?? '';
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
              content: Text(appliedFormat == 'cla=1'
                  ? '已按 Clash 格式获取节点；Windows 系统代理保持关闭'
                  : '节点已获取；Windows 系统代理保持关闭')),
        );
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _hasFetchedNodes = true;
          _error = e.toString().replaceFirst('Exception: ', '');
        });
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _selectNode(String name) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('PUT', '/api/nodes/select', body: {'name': name});
      // Choosing a node can move the region lock, which changes the candidate
      // pool, so both the state and the region table have to be re-read.
      await _loadState();
      if (mounted) {
        setState(() {
          _selectedNode = name;
          _diagnose = null;
          _speed = null;
        });
        if (!_connected) {
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(
                content: Text(name == autoGroup
                    ? '已设为自动选择'
                    : name == fallbackGroup
                        ? '已设为故障转移'
                        : '已保存此节点，连接时将应用')),
          );
        }
      }
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// Parses or stores one pasted link. The local service owns the parsing, so
  /// this only carries the text and reports what came back.
  Future<(ManualNode?, String?)> _manualNodeRequest(
      String uri, String name, bool commit) async {
    try {
      final value = await _request('POST', '/api/nodes/manual', body: {
        'uri': uri,
        if (name.trim().isNotEmpty) 'name': name.trim(),
        if (!commit) 'preview': true,
      });
      final node = ManualNode.fromJson(
          value['node'] as Map<String, dynamic>? ?? const {});
      if (commit) {
        await _loadNodes();
      }
      return (node, null);
    } catch (e) {
      return (null, e.toString().replaceFirst('Exception: ', ''));
    }
  }

  Future<void> _showAddNodeDialog() async {
    final added = await showDialog<ManualNode>(
      context: context,
      builder: (context) => _AddNodeDialog(request: _manualNodeRequest),
    );
    if (added == null || !mounted) return;
    ScaffoldMessenger.of(context)
        .showSnackBar(SnackBar(content: Text('已添加 ${parsedNodeLabel(added)}')));
  }

  Future<void> _deleteManualNode(String name) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('删除手动节点「$name」？'),
        content: const Text('只从本地配置里移除这个节点，订阅节点不受影响。'),
        actions: [
          TextButton(
              onPressed: () => Navigator.of(context).pop(false),
              child: const Text('取消')),
          TextButton(
              onPressed: () => Navigator.of(context).pop(true),
              child: const Text('删除')),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await _request(
          'DELETE', '/api/nodes/manual?name=${Uri.encodeQueryComponent(name)}');
      await _loadNodes();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  Future<void> _measureLatency() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final result = await _request('GET', '/api/latency');
      setState(() => _latency = result['latencyMs'] as int?);
    } catch (e) {
      setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// Measures every node in one kernel call, which is the only way to get
  /// comparable numbers for nodes the health sweep never touches.
  Future<void> _measureAllNodes() async {
    setState(() {
      _busy = true;
      _measuringNodes = true;
      _error = null;
    });
    try {
      final value = await _request('GET', '/api/nodes/latency');
      if (!mounted) return;
      final raw = value['latency'] as Map<String, dynamic>? ?? const {};
      setState(() {
        _nodeLatency =
            raw.map((key, item) => MapEntry(key, (item as num?)?.toInt() ?? 0));
      });
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) {
        setState(() {
          _busy = false;
          _measuringNodes = false;
        });
      }
    }
  }

  /// One pass over everything the check page shows: the exit and who runs it, the
  /// resolver that answers for us, whether UDP leaves through the same address,
  /// and whether the sites the subscription is bought for are reachable.
  Future<void> _runDiagnose() async {
    setState(() {
      _diagnosing = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/diagnose');
      if (!mounted) return;
      setState(() => _diagnose = DiagnoseReport.fromJson(value));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _diagnosing = false);
    }
  }

  /// Measures throughput through the node in use. The size the user picked is the
  /// download allowance; the upload is a third of it.
  Future<void> _runSpeedTest() async {
    setState(() {
      _speedRunning = true;
      _error = null;
      _speed = null;
    });
    try {
      final value = await _request('POST', '/api/speedtest',
          body: {'megaBytes': _speedMegaBytes});
      if (!mounted) return;
      final speed = value['speed'] as Map<String, dynamic>? ?? const {};
      setState(() => _speed = SpeedTestResult.fromJson(speed));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _speedRunning = false);
    }
  }

  /// Puts the same rows on the clipboard as text, so a result can be pasted into
  /// a message instead of described.
  Future<void> _copyReport() async {
    final report = _diagnose;
    if (report == null) return;
    await Clipboard.setData(
        ClipboardData(text: diagnoseReportText(report, speed: _speed)));
    if (!mounted) return;
    ScaffoldMessenger.of(context)
        .showSnackBar(const SnackBar(content: Text('检测报告已复制到剪贴板')));
  }

  /// The path monitor's configuration and its last round. The page polls this
  /// while it is open, so a switch can be watched as it happens.
  Future<void> _loadPathChecks() async {
    if (_port == null) return;
    try {
      final value = await _request('GET', '/api/path-checks');
      if (!mounted) return;
      final checks = PathChecks.fromJson(
          value['settings'] as Map<String, dynamic>? ?? const {});
      setState(() {
        _pathChecks = checks;
        _pathStatus = PathStatus.fromJson(
            value['status'] as Map<String, dynamic>? ?? const {});
        _pathLimits = PathLimits.fromJson(
            value['limits'] as Map<String, dynamic>? ?? const {});
        // The interval field follows the stored value unless it is being typed in.
        if (!_pathInterval.selection.isValid || _pathInterval.text.isEmpty) {
          _pathInterval.text = '${checks.intervalSec}';
        }
      });
      final status = _pathStatus;
      if (status.alert.isNotEmpty &&
          status.alertId > _shownPathAlertId &&
          !_pathAlertOpen) {
        _shownPathAlertId = status.alertId;
        unawaited(_showPathRecoveryAlert(status));
      }
    } catch (_) {
      // The connection page already reports a service that has gone away.
    }
  }

  /// Saving the monitor is one call: the service validates, stores, and restarts
  /// its timer, and answers with what it accepted.
  Future<void> _savePathChecks(PathChecks checks) async {
    setState(() => _pathSaving = true);
    try {
      final value =
          await _request('PUT', '/api/path-checks', body: checks.toJson());
      if (!mounted) return;
      final saved = PathChecks.fromJson(
          value['settings'] as Map<String, dynamic>? ?? const {});
      setState(() => _pathChecks = saved);
      await _loadPathChecks();
      // Switching the monitor on or off changes whether the page needs to poll.
      _syncPathPoll();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _pathSaving = false);
    }
  }

  /// The interval is typed, so it is applied on request rather than on every
  /// keystroke; the service clamps it either way.
  Future<void> _applyPathInterval() async {
    final parsed = int.tryParse(_pathInterval.text.trim());
    if (parsed == null) {
      setState(() => _error = '间隔需要是一个秒数');
      return;
    }
    await _savePathChecks(PathChecks(
      enabled: _pathChecks.enabled,
      intervalSec: parsed,
      targets: _pathChecks.targets,
    ));
  }

  Future<void> _runPathChecksNow() async {
    try {
      await _request('POST', '/api/path-checks/run');
      await _loadPathChecks();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  void _togglePathTarget(PathTarget target) {
    final updated = [
      for (final entry in _pathChecks.targets)
        entry.url == target.url ? entry.withEnabled(!entry.enabled) : entry,
    ];
    unawaited(_savePathChecks(_pathChecks.withTargets(updated)));
  }

  void _removePathTarget(PathTarget target) {
    final updated = [
      for (final entry in _pathChecks.targets)
        if (entry.url != target.url) entry,
    ];
    unawaited(_savePathChecks(_pathChecks.withTargets(updated)));
  }

  Future<void> _addPathTarget() async {
    final url = _pathURL.text.trim();
    if (url.isEmpty) return;
    final label = _pathLabel.text.trim();
    final updated = [
      ..._pathChecks.targets,
      PathTarget(
          label: label.isEmpty ? url : label,
          url: url,
          builtin: false,
          enabled: true),
    ];
    _pathLabel.clear();
    _pathURL.clear();
    await _savePathChecks(_pathChecks.withTargets(updated));
  }

  /// The status only changes while a monitor is running, so it is polled while
  /// the page that shows it is open, and stopped everywhere else.
  void _syncPathPoll() {
    final wanted = _pathChecks.enabled && _port != null;
    if (!wanted) {
      _pathTimer?.cancel();
      _pathTimer = null;
      return;
    }
    _pathTimer ??= Timer.periodic(
        const Duration(seconds: 2), (_) => unawaited(_loadPathChecks()));
  }

  Future<void> _showPathRecoveryAlert(PathStatus status) async {
    if (!mounted || _pathAlertOpen) return;
    _pathAlertOpen = true;
    final parsedDue = DateTime.tryParse(status.retryAt)?.toLocal();
    final due = parsedDue ?? DateTime.now().add(const Duration(seconds: 30));
    var remaining = due.difference(DateTime.now()).inSeconds.clamp(0, 30);
    Timer? ticker;
    var dialogOpen = true;
    try {
      await showDialog<void>(
        context: context,
        barrierDismissible: false,
        builder: (dialogContext) => StatefulBuilder(
          builder: (dialogContext, setDialogState) {
            ticker ??= Timer.periodic(const Duration(seconds: 1), (_) {
              if (!dialogOpen || !dialogContext.mounted) return;
              final next =
                  due.difference(DateTime.now()).inSeconds.clamp(0, 30);
              setDialogState(() => remaining = next);
              if (next == 0) {
                dialogOpen = false;
                ticker?.cancel();
                Navigator.of(dialogContext).pop();
              }
            });
            return AlertDialog(
              title: const Text('定向通路检测未通过'),
              content: Column(mainAxisSize: MainAxisSize.min, children: [
                Text(status.alert),
                const SizedBox(height: 14),
                Text('$remaining 秒后自动继续检测同地区节点。'),
              ]),
              actions: [
                TextButton(
                  onPressed: () async {
                    await _request('POST', '/api/path-checks/recovery/cancel');
                    dialogOpen = false;
                    if (dialogContext.mounted)
                      Navigator.of(dialogContext).pop();
                    await _loadPathChecks();
                  },
                  child: const Text('取消自动检测'),
                ),
                FilledButton(
                  onPressed: () async {
                    await _request('POST', '/api/path-checks/run');
                    dialogOpen = false;
                    if (dialogContext.mounted)
                      Navigator.of(dialogContext).pop();
                    await _loadPathChecks();
                  },
                  child: const Text('立即检测'),
                ),
              ],
            );
          },
        ),
      );
    } catch (_) {
      // The service may stop while the prompt is open; the next state poll
      // refreshes the status without blocking the application.
    } finally {
      dialogOpen = false;
      ticker?.cancel();
      _pathAlertOpen = false;
    }
  }

  /// The saved subscriptions, which one is in use, and whether the profile in
  /// place came from a cached copy rather than a fresh download.
  Future<void> _loadSubscriptions() async {
    if (_port == null) return;
    try {
      final value = await _request('GET', '/api/subscriptions');
      if (!mounted) return;
      setState(() {
        _savedSubscriptions =
            (value['subscriptions'] as List<dynamic>? ?? const [])
                .whereType<Map<String, dynamic>>()
                .map(SavedSubscription.fromJson)
                .toList(growable: false);
        _subscriptionsFromCache = value['fromCache'] as bool? ?? false;
        _kernelRunning = value['running'] as bool? ?? false;
        _autoMergeHours = (value['autoMergeHours'] as num?)?.toInt() ?? 0;
      });
    } catch (_) {
      // The connection page already reports a service that has gone away.
    }
  }

  Future<void> _setAutoMergeHours(int hours) async {
    setState(() => _autoMergeHours = hours);
    try {
      await _request('POST', '/api/subscriptions/auto-merge',
          body: {'hours': hours});
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text(hours > 0 ? '已开启定时自动合并（每 $hours 小时）' : '已关闭定时自动合并'),
      ));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  /// Switching to another subscription replaces the whole node set on the
  /// service side, so everything derived from the old one is reloaded here
  /// rather than patched.
  Future<void> _activateSubscription(SavedSubscription entry) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/subscriptions/activate',
          body: {'id': entry.id});
      if (!mounted) return;
      final fromCache = value['fromCache'] as bool? ?? false;
      await _loadState();
      await _loadSubscriptions();
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
          content: Text(fromCache
              ? '已切换到「${entry.label}」，但拉取失败，用的是上次的缓存副本'
              : '已切换到「${entry.label}」')));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _renameSubscription(SavedSubscription entry) async {
    final controller = TextEditingController(text: entry.label);
    final label = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('给这个订阅改名'),
        content: TextField(
          controller: controller,
          autofocus: true,
          decoration: const InputDecoration(labelText: '名称'),
        ),
        actions: [
          TextButton(
              onPressed: () => Navigator.of(context).pop(),
              child: const Text('取消')),
          TextButton(
              onPressed: () => Navigator.of(context).pop(controller.text),
              child: const Text('保存')),
        ],
      ),
    );
    controller.dispose();
    final trimmed = (label ?? '').trim();
    if (trimmed.isEmpty || trimmed == entry.label) return;
    try {
      await _request('PUT', '/api/subscriptions',
          body: {'id': entry.id, 'label': trimmed});
      await _loadSubscriptions();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  Future<void> _deleteSubscription(SavedSubscription entry) async {
    final active = entry.url == _savedSubscriptionUrl;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('删除订阅「${entry.label}」？'),
        content: Text(active
            ? '这正是当前使用的订阅，它的本地配置、节点缓存、地区验证与健康数据会一起删除。手动添加的节点不受影响。'
            : '只删除这条保存的地址，本地配置不受影响。'),
        actions: [
          TextButton(
              onPressed: () => Navigator.of(context).pop(false),
              child: const Text('取消')),
          TextButton(
              onPressed: () => Navigator.of(context).pop(true),
              child: const Text('删除')),
        ],
      ),
    );
    if (confirmed != true) return;
    try {
      await _request('DELETE',
          '/api/subscriptions?id=${Uri.encodeQueryComponent(entry.id)}');
      await _loadState();
      await _loadSubscriptions();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  Future<void> _mergeAllSubscriptions() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/subscriptions/merge');
      final nodes = (value['nodes'] as num?)?.toInt() ?? 0;
      _savedSubscriptionUrl = '__merged__';
      _subscription.clear();
      await _loadState();
      await _loadSubscriptions();
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text('已合并更新所有启用订阅并激活为主源，共获取 $nodes 个可用节点'),
      ));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _activateMergedSubscription() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/subscriptions/activate',
          body: {'id': '__merged__'});
      _savedSubscriptionUrl = '__merged__';
      _subscription.clear();
      await _loadState();
      await _loadSubscriptions();
      if (!mounted) return;
      final nodes = (value['nodes'] as num?)?.toInt() ?? 0;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text('已切回多订阅合并主源，共获取 $nodes 个可用节点'),
      ));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _toggleSubscriptionEnabled(
      SavedSubscription entry, bool enabled) async {
    try {
      await _request('POST', '/api/subscriptions/update', body: {
        'id': entry.id,
        'enabled': enabled,
      });
      await _loadSubscriptions();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  Future<void> _refreshSingleSubscription(SavedSubscription entry) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final res = await _request('POST', '/api/subscriptions/refresh', body: {
        'id': entry.id,
      });
      await _loadSubscriptions();
      if (!mounted) return;
      final sub = res['subscription'] is Map<String, dynamic>
          ? SavedSubscription.fromJson(
              res['subscription'] as Map<String, dynamic>)
          : null;
      final count = sub?.nodeCount ?? 0;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text('「${entry.label}」刷新完成，可用节点：$count'),
      ));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _showAddSubscriptionDialog() async {
    final urlController = TextEditingController();
    final nameController = TextEditingController();
    final prefixController = TextEditingController();

    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('添加新订阅或节点'),
        content: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              TextField(
                controller: urlController,
                autofocus: true,
                decoration: const InputDecoration(
                  labelText: '订阅链接或节点链接 (必填)',
                  hintText: 'https://... 或 vmess://, vless://, hysteria2://...',
                  prefixIcon: Icon(Icons.link_rounded, size: 18),
                ),
                maxLines: 2,
              ),
              const SizedBox(height: 12),
              TextField(
                controller: nameController,
                decoration: const InputDecoration(
                  labelText: '自定义名称 (选填)',
                  hintText: '留空则自动根据域名或节点解析',
                  prefixIcon: Icon(Icons.label_outline, size: 18),
                ),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: prefixController,
                decoration: const InputDecoration(
                  labelText: '节点前缀 (选填，合并时自动加在节点名前)',
                  hintText: '例如: [主力] 或 [备用]',
                  prefixIcon: Icon(Icons.tag, size: 18),
                ),
              ),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('添加'),
          ),
        ],
      ),
    );

    final rawURL = urlController.text.trim();
    final label = nameController.text.trim();
    final prefix = prefixController.text.trim();
    urlController.dispose();
    nameController.dispose();
    prefixController.dispose();

    if (confirmed != true || rawURL.isEmpty) return;

    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('POST', '/api/subscriptions/add', body: {
        'url': rawURL,
        'label': label,
        'prefix': prefix,
        'enabled': true,
      });
      await _loadSubscriptions();
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(
        content: Text('已添加新订阅'),
      ));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _editSubscription(SavedSubscription entry) async {
    final nameController = TextEditingController(text: entry.label);
    final prefixController = TextEditingController(text: entry.prefix);

    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('编辑订阅信息'),
        content: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              TextField(
                controller: nameController,
                autofocus: true,
                decoration: const InputDecoration(
                  labelText: '名称',
                  prefixIcon: Icon(Icons.label_outline, size: 18),
                ),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: prefixController,
                decoration: const InputDecoration(
                  labelText: '节点前缀 (合并时自动附在节点名前)',
                  hintText: '例如: [主力]',
                  prefixIcon: Icon(Icons.tag, size: 18),
                ),
              ),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('保存'),
          ),
        ],
      ),
    );

    final label = nameController.text.trim();
    final prefix = prefixController.text.trim();
    nameController.dispose();
    prefixController.dispose();

    if (confirmed != true) return;
    if (label.isEmpty && prefix == entry.prefix) return;

    try {
      await _request('POST', '/api/subscriptions/update', body: {
        'id': entry.id,
        'label': label.isNotEmpty ? label : entry.label,
        'prefix': prefix,
      });
      await _loadSubscriptions();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    }
  }

  Future<void> _loadRegions() async {
    final value = await _request('GET', '/api/regions');
    if (!mounted) return;
    final nodes = (value['nodes'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(RegionInfo.fromJson)
        .toList(growable: false);
    final rawHealth = value['health'] as Map<String, dynamic>? ?? const {};
    final health = <String, NodeHealth>{};
    rawHealth.forEach((name, item) {
      if (item is Map<String, dynamic>) {
        health[name] = NodeHealth.fromJson(item);
      }
    });
    setState(() {
      _regionNodes = nodes;
      _regionByName = {for (final node in nodes) node.name: node};
      _nodeHealth = health;
      _regionPool = (value['pool'] as List<dynamic>? ?? const [])
          .whereType<String>()
          .toList(growable: false);
      _lockedRegion = value['lockedRegion'] as String? ?? _lockedRegion;
      _regionJob =
          RegionJob.fromJson(value['job'] as Map<String, dynamic>? ?? const {});
    });
  }

  Future<void> _verifyRegions({bool refresh = false}) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/regions/verify',
          body: {'refresh': refresh});
      await _loadRegions();
      if (!mounted) return;
      final job =
          RegionJob.fromJson(value['job'] as Map<String, dynamic>? ?? const {});
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
            content:
                Text(job.running ? '已开始验证出口地区，进度显示在「地区与健康」' : '出口地区验证已结束')),
      );
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _lockRegion(String country) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('POST', '/api/region/lock', body: {'country': country});
      await _loadState();
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
            content: Text(
                country.isEmpty ? '已取消地区锁定，同地区故障切换不再生效' : '已锁定出口地区：$country')),
      );
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _patrolNow() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('POST', '/api/health/patrol');
      await _loadState();
      await _loadRegions();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _saveHealthParams(HealthParams params) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('PUT', '/api/health/params', body: params.toJson());
      await _loadState();
      if (!mounted) return;
      ScaffoldMessenger.of(context)
          .showSnackBar(const SnackBar(content: Text('健康策略已保存')));
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _loadTun() async {
    try {
      final value = await _request('GET', '/api/tun');
      if (!mounted) return;
      setState(() => _tun = TunStatus.fromJson(value));
    } catch (_) {
      // The rail and the subscription page tolerate a stale TUN report.
    }
  }

  Future<void> _setConnectionMode(String mode) async {
    if (mode == _connectionMode) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _request('PUT', '/api/settings', body: {
        'subscriptionUrl': _subscription.text.trim(),
        'mihomoPath': _mihomo.text.trim(),
        'connectionMode': mode,
        'wintunPath': _wintun.text.trim(),
      });
      await _loadState();
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _downloadWintun() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/tun/wintun',
          body: {'path': _wintun.text.trim()});
      await _loadTun();
      await _loadState();
      if (!mounted) return;
      final wintun = WintunStatus.fromJson(
          value['wintun'] as Map<String, dynamic>? ?? const {});
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('wintun.dll 已就绪：${wintun.path}')),
      );
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _runTunChecks() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('POST', '/api/tun/check');
      if (!mounted) return;
      setState(() {
        _tunChecks = (value['checks'] as List<dynamic>? ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(TunCheck.fromJson)
            .toList(growable: false);
        _tunChecksActive = value['active'] as bool? ?? false;
      });
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// Relaunching is the only way to gain the administrator token: an already
  /// running process cannot raise its own privileges.
  Future<void> _relaunchElevated() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      if (_connected) {
        await _request('POST', '/api/disconnect');
      }
      // A path passed after -Command does not reliably reach the script, so it
      // is interpolated as a single-quoted PowerShell literal instead.
      final quoted = "'${Platform.resolvedExecutable.replaceAll("'", "''")}'";
      final result = await Process.run('powershell.exe', [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        'Start-Process -FilePath $quoted -Verb RunAs',
      ]);
      if (result.exitCode != 0) {
        throw Exception('提权启动失败：${result.stderr.toString().trim()}');
      }
      await _request('POST', '/api/shutdown')
          .catchError((_) => <String, dynamic>{});
      exit(0);
    } catch (e) {
      if (mounted) {
        setState(() {
          _busy = false;
          _error = e.toString().replaceFirst('Exception: ', '');
        });
      }
    }
  }

  // -------------------------------------------------------------------------
  // Dialogs
  // -------------------------------------------------------------------------

  Future<void> _showEvents() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final value = await _request('GET', '/api/events?limit=50');
      if (!mounted) return;
      final events = (value['events'] as List<dynamic>? ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(SwitchEvent.fromJson)
          .toList(growable: false);
      await showDialog<void>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('切换事件'),
          content: SizedBox(
            width: dialogWidth(context, 520),
            child: SingleChildScrollView(
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    if (events.isEmpty)
                      const Text('还没有发生节点切换。')
                    else
                      ...events.map((event) => Padding(
                            padding: const EdgeInsets.only(bottom: 12),
                            child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Row(children: [
                                    Text(event.at, style: Tokens.number),
                                    const SizedBox(width: 10),
                                    _Badge(
                                        label:
                                            switchTriggerLabel(event.trigger),
                                        color:
                                            event.trigger == 'region_exhausted'
                                                ? Tokens.bad
                                                : Tokens.inkMuted,
                                        tint:
                                            event.trigger == 'region_exhausted'
                                                ? Tokens.badTint
                                                : Tokens.idleTint),
                                  ]),
                                  const SizedBox(height: 4),
                                  Text('${event.fromNode} → ${event.toNode}'),
                                  if (event.evidence.isNotEmpty)
                                    Text(event.evidence, style: Tokens.hint),
                                ]),
                          )),
                    const SizedBox(height: 4),
                    const Text('切换只影响新连接；已建立的连接可能断开。',
                        style: TextStyle(color: Tokens.inkMuted, fontSize: 12)),
                  ]),
            ),
          ),
          actions: [
            TextButton(
                onPressed: () => Navigator.pop(context),
                child: const Text('关闭')),
          ],
        ),
      );
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _showHealthParams() async {
    final current = _healthParams;
    final controllers = <TextEditingController>[
      TextEditingController(text: '${current.connectTimeoutMs}'),
      TextEditingController(text: '${current.patrolIntervalSec}'),
      TextEditingController(text: '${current.failureThreshold}'),
      TextEditingController(text: '${current.cooldownSec}'),
      TextEditingController(text: '${current.minDwellSec}'),
      TextEditingController(text: '${current.recoverySuccesses}'),
    ];
    const labels = [
      '连接检测超时（毫秒）',
      '巡检间隔（秒）',
      '失败确认次数',
      '冷却时间（秒）',
      '最短驻留（秒）',
      '恢复所需连续成功次数',
    ];
    try {
      final save = await showDialog<bool>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('健康策略'),
          content: SizedBox(
            width: dialogWidth(context, 420),
            child: SingleChildScrollView(
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const Text('这些数值决定巡检频率、失败确认与冷却时间，需在真实网络中校准。',
                        style: TextStyle(color: Tokens.inkMuted, fontSize: 12)),
                    const SizedBox(height: 16),
                    for (var index = 0; index < controllers.length; index++)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 10),
                        child: TextField(
                          controller: controllers[index],
                          keyboardType: TextInputType.number,
                          decoration: InputDecoration(labelText: labels[index]),
                        ),
                      ),
                  ]),
            ),
          ),
          actions: [
            TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: const Text('取消')),
            FilledButton(
                onPressed: () => Navigator.pop(context, true),
                child: const Text('保存')),
          ],
        ),
      );
      if (save != true) return;
      int read(int index, int fallback) =>
          int.tryParse(controllers[index].text.trim()) ?? fallback;
      await _saveHealthParams(HealthParams(
        connectTimeoutMs: read(0, current.connectTimeoutMs),
        patrolIntervalSec: read(1, current.patrolIntervalSec),
        failureThreshold: read(2, current.failureThreshold),
        cooldownSec: read(3, current.cooldownSec),
        minDwellSec: read(4, current.minDwellSec),
        recoverySuccesses: read(5, current.recoverySuccesses),
      ));
    } finally {
      for (final controller in controllers) {
        controller.dispose();
      }
    }
  }

  Future<void> _showAllRegions() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _loadRegions();
      if (!mounted) return;
      final usable = _regionNodes.where((node) => node.usable).length;
      await showDialog<void>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('出口地区验证结果'),
          content: SizedBox(
            width: dialogWidth(context, 560),
            child: SingleChildScrollView(
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    _DiagnosticSection('汇总', [
                      '已验证可用：$usable / ${_regionNodes.length}',
                      '锁定地区：${_lockedRegion.isEmpty ? '未锁定' : _lockedRegion}',
                      '候选节点：$_poolSize',
                      '验证有效期：24 小时',
                    ]),
                    const SizedBox(height: 16),
                    if (_regionNodes.isEmpty)
                      const Text('还没有验证结果。连接后点击「验证出口地区」。')
                    else
                      ..._regionNodes.map((node) => Padding(
                            padding: const EdgeInsets.only(bottom: 4),
                            child: MediaQuery.sizeOf(context).width <
                                    Tokens.mobileBreakpoint
                                ? Row(
                                    crossAxisAlignment:
                                        CrossAxisAlignment.start,
                                    children: [
                                        SizedBox(
                                          width: 60,
                                          child: Text(
                                              node.country.isEmpty
                                                  ? '未知'
                                                  : node.country,
                                              style: Tokens.number),
                                        ),
                                        SizedBox(
                                          width: 88,
                                          child: Text(
                                              regionStatusLabel(node.status),
                                              style: TextStyle(
                                                  fontSize: 12,
                                                  color: node.usable
                                                      ? Tokens.ok
                                                      : Tokens.warn)),
                                        ),
                                        // The name and the evidence go on one
                                        // another: side by side on a phone, the
                                        // evidence is what gets pushed off the
                                        // row, and it is the part that says why
                                        // a node was left out.
                                        Expanded(
                                          child: Column(
                                              crossAxisAlignment:
                                                  CrossAxisAlignment.start,
                                              children: [
                                                Text(node.name,
                                                    overflow:
                                                        TextOverflow.ellipsis,
                                                    style: const TextStyle(
                                                        fontSize: 12.5)),
                                                if (node.detail.isNotEmpty)
                                                  Text(
                                                      node.stale
                                                          ? '已过期'
                                                          : node.detail,
                                                      style: Tokens.hint),
                                              ]),
                                        ),
                                      ])
                                : Row(children: [
                                    SizedBox(
                                      width: 74,
                                      child: Text(
                                          node.country.isEmpty
                                              ? '未知'
                                              : node.country,
                                          style: Tokens.number),
                                    ),
                                    SizedBox(
                                      width: 96,
                                      child: Text(
                                          regionStatusLabel(node.status),
                                          style: TextStyle(
                                              fontSize: 12,
                                              color: node.usable
                                                  ? Tokens.ok
                                                  : Tokens.warn)),
                                    ),
                                    Expanded(
                                        child: Text(node.name,
                                            overflow: TextOverflow.ellipsis,
                                            style: const TextStyle(
                                                fontSize: 12.5))),
                                    if (node.detail.isNotEmpty)
                                      Text(node.stale ? '已过期' : node.detail,
                                          style: Tokens.hint),
                                  ]),
                          )),
                    const SizedBox(height: 12),
                    const Text('只有两个数据源一致、且未过期的节点才能进入锁定地区的候选池。',
                        style: TextStyle(color: Tokens.inkMuted, fontSize: 12)),
                  ]),
            ),
          ),
          actions: [
            TextButton(
                onPressed: () => Navigator.pop(context),
                child: const Text('关闭')),
          ],
        ),
      );
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _showDiagnostics() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final report = await _request('GET', '/api/diagnostics/subscription');
      if (!mounted) return;
      final fetch = report['fetch'] as Map<String, dynamic>? ?? const {};
      final payload = fetch['payload'] as Map<String, dynamic>? ?? const {};
      final cache = report['cache'] as Map<String, dynamic>? ?? const {};
      final cacheNetworks =
          cache['networks'] as Map<String, dynamic>? ?? const {};
      final cacheTls = cache['tlsModes'] as Map<String, dynamic>? ?? const {};
      final queryKeys =
          (report['requestQueryKeys'] as List<dynamic>? ?? const [])
              .whereType<String>()
              .join('，');
      final format = formatLabel(payload['format'] as String? ?? '');
      final appliedFormat = report['appliedFormat'] as String? ?? '';

      await showDialog<void>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('订阅诊断'),
          content: SizedBox(
            width: dialogWidth(context, 520),
            child: SingleChildScrollView(
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    _DiagnosticSection('请求', [
                      '订阅参数名：${queryKeys.isEmpty ? '无' : queryKeys}',
                      '客户端标识：${report['userAgent'] ?? '未知'}',
                      '追加 Clash 标识：${report['flagAdded'] == true ? '是' : '否'}',
                    ]),
                    const SizedBox(height: 16),
                    _DiagnosticSection('本次响应', [
                      'HTTP 状态：${fetch['statusCode'] ?? '未完成'}',
                      '响应类型：${fetch['contentType'] ?? '未知'}',
                      '响应大小：${byteLabel(fetch['bytes'] as int? ?? 0)}',
                      '识别格式：$format',
                      '识别到的 URI：${uriCountLabel(payload['uriCounts'])}',
                      '可选节点：${payload['selectableNodes'] ?? 0}',
                      if (fetch['error'] is String) fetch['error'] as String,
                    ]),
                    const SizedBox(height: 16),
                    _DiagnosticSection('Mihomo 缓存', [
                      '已采用格式：${appliedFormat == 'cla=1' ? 'Clash（cla=1）' : (appliedFormat.isEmpty ? '尚未缓存' : appliedFormat)}',
                      '缓存格式：${formatLabel(cache['format'] as String? ?? '')}',
                      '可选节点：${cache['selectableNodes'] ?? 0}',
                      'VMess 节点：${cache['validVmess'] ?? 0}　信息公告条目：${cache['infoEntries'] ?? 0}',
                      '传输方式：${countsLabel(cacheNetworks)}',
                      'TLS：${countsLabel(cacheTls)}',
                      'WS Host 已配置：${cache['wsHostSet'] ?? 0}　Path 已配置：${cache['wsPathSet'] ?? 0}',
                    ]),
                    const SizedBox(height: 12),
                    const Text('诊断不会显示订阅参数值、节点地址或 UUID。',
                        style: TextStyle(color: Tokens.inkMuted, fontSize: 12)),
                  ]),
            ),
          ),
          actions: [
            TextButton(
                onPressed: () => Navigator.pop(context),
                child: const Text('关闭')),
          ],
        ),
      );
    } catch (e) {
      if (mounted) {
        setState(() => _error = e.toString().replaceFirst('Exception: ', ''));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  // -------------------------------------------------------------------------
  // Shell
  // -------------------------------------------------------------------------

  @override
  Widget build(BuildContext context) {
    // A narrow window is a phone, and the rail is a layout for something wider:
    // it takes a fixed width that a phone does not have, and what would be left
    // for the page is not enough to draw in. The pages are the same either way —
    // what changes is where the list of them lives.
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return Scaffold(
      appBar: narrow
          ? AppBar(
              backgroundColor: Tokens.rail,
              foregroundColor: Tokens.ink,
              elevation: 0,
              title: const Text('SmartVPN',
                  style: TextStyle(
                      fontSize: 16,
                      fontWeight: FontWeight.w600,
                      letterSpacing: 0.2)),
              actions: [
                Padding(
                    padding: const EdgeInsets.only(right: 12),
                    child: _statusBadge()),
              ],
            )
          : null,
      drawer: narrow ? Drawer(child: SafeArea(child: _rail(inDrawer: true))) : null,
      body: SafeArea(
        child: Stack(
          children: [
            narrow
                ? _detail()
                : Row(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                        _rail(inDrawer: false),
                        Expanded(child: _detail()),
                      ]),
          ],
        ),
      ),
    );
  }

  Widget _rail({required bool inDrawer}) => LayoutBuilder(
        builder: (context, constraints) => Container(
          width: Tokens.railWidth,
          decoration: const BoxDecoration(
            color: Tokens.rail,
            border: Border(right: BorderSide(color: Tokens.hairline)),
          ),
          child: SingleChildScrollView(
            child: ConstrainedBox(
              constraints: BoxConstraints(minHeight: constraints.maxHeight),
              child: IntrinsicHeight(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Padding(
                      padding: const EdgeInsets.fromLTRB(18, 16, 18, 12),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text('// RHODES ISLAND · PRTS',
                              style: Tokens.akOverline
                                  .copyWith(fontSize: 9, letterSpacing: 1.2)),
                          const SizedBox(height: 6),
                          Row(
                            children: [
                              Container(
                                width: Tokens.barWidth,
                                height: 15,
                                decoration: BoxDecoration(
                                  color: Tokens.cyan,
                                  borderRadius: BorderRadius.circular(1.0),
                                ),
                              ),
                              const SizedBox(width: 8),
                              const Expanded(
                                child: Text('SmartVPN',
                                    style: TextStyle(
                                        fontSize: 15,
                                        fontWeight: FontWeight.w700,
                                        letterSpacing: 0.4)),
                              ),
                            ],
                          ),
                          const SizedBox(height: 6),
                          Row(children: [
                            _StatusDot(
                                color: _blocked
                                    ? Tokens.bad
                                    : (_protectedNow ? Tokens.ok : Tokens.inkFaint)),
                            const SizedBox(width: 8),
                            Text(_railStateLabel(), style: Tokens.hint),
                          ]),
                        ],
                      ),
                    ),
                    const Divider(),
                    const SizedBox(height: 6),
                    for (final page in _AppPage.values)
                      _navItem(page, inDrawer: inDrawer),
                    const Spacer(),
                    if (Platform.isWindows)
                      Padding(
                        padding: const EdgeInsets.symmetric(
                            horizontal: 12, vertical: 4),
                        child: Row(children: [
                          Expanded(
                            child: InkWell(
                              onTap: () => _configureDesktopPet(
                                  enabled: !_desktopPetEnabled),
                              borderRadius:
                                  BorderRadius.circular(Tokens.radiusBadge),
                              child: Container(
                                padding: const EdgeInsets.symmetric(
                                    horizontal: 10, vertical: 9),
                                decoration: BoxDecoration(
                                  color: _desktopPetEnabled
                                      ? Tokens.cyan.withValues(alpha: 0.08)
                                      : Colors.transparent,
                                  border: Border.all(
                                    color: _desktopPetEnabled
                                        ? Tokens.cyan.withValues(alpha: 0.5)
                                        : Tokens.hairline,
                                    width: 0.8,
                                  ),
                                  borderRadius:
                                      BorderRadius.circular(Tokens.radiusBadge),
                                ),
                                child: Row(children: [
                                  Icon(
                                    _desktopPetEnabled
                                        ? Icons.pets_rounded
                                        : Icons.pets_outlined,
                                    size: 15,
                                    color: _desktopPetEnabled
                                        ? Tokens.cyan
                                        : Tokens.inkMuted,
                                  ),
                                  const SizedBox(width: 8),
                                  Expanded(
                                    child: Text(
                                      _desktopPetEnabled
                                          ? '阿米娅桌宠 已开启'
                                          : '阿米娅桌宠 已关闭',
                                      style: TextStyle(
                                        fontSize: 11,
                                        fontWeight: _desktopPetEnabled
                                            ? FontWeight.w700
                                            : FontWeight.w500,
                                        color: _desktopPetEnabled
                                            ? Tokens.cyan
                                            : Tokens.inkMuted,
                                      ),
                                    ),
                                  ),
                                ]),
                              ),
                            ),
                          ),
                          const SizedBox(width: 4),
                          IconButton(
                            tooltip: '桌宠设置',
                            onPressed: _showDesktopPetSettings,
                            icon: const Icon(Icons.tune_rounded, size: 18),
                            visualDensity: VisualDensity.compact,
                          ),
                        ]),
                      ),
                    const Divider(),
                    _AkStripes(
                      height: 2.5,
                      color: _connected
                          ? Tokens.cyan.withOpacity(0.5)
                          : Tokens.hairlineBright,
                      background: Colors.transparent,
                    ),
                    Padding(
                      padding: const EdgeInsets.fromLTRB(18, 12, 18, 14),
                      child: Text(
                        _connected
                            ? '内核运行中　127.0.0.1:$_mixedPort'
                            : '内核未运行',
                        style: Tokens.hint,
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        ),
      );

  String _railStateLabel() {
    if (_blocked) return '已阻断';
    if (_protectedNow) return '已连接';
    if (_connected) return '内核运行中';
    return '未连接';
  }

  Widget _navItem(_AppPage page, {required bool inDrawer}) {
    final count = switch (page) {
      _AppPage.nodes => _nodes.isEmpty ? null : '${_nodes.length}',
      _AppPage.region => _lockedRegion.isEmpty ? null : '$_poolSize',
      _ => null,
    };
    return _AkHoverNavItem(
      page: page,
      selected: page == _page,
      count: count,
      onTap: () {
        _goTo(page);
        if (inDrawer) Navigator.of(context).pop();
      },
    );
  }

  Widget _detail() {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return Column(children: [
        Padding(
          padding: EdgeInsets.fromLTRB(
              narrow ? 14 : 28,
              narrow ? 12 : 20,
              narrow ? 14 : 28,
              narrow ? 10 : 16),
          child: Center(
            child: ConstrainedBox(
              constraints:
                  const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
              child: Row(children: [
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Text(_page.label, style: Tokens.pageTitle),
                    const SizedBox(height: 2),
                    Text('// PRTS · ${_page.englishCode}',
                        style: Tokens.akBilingualEn),
                  ],
                ),
                if (!narrow) ...[
                  const Spacer(),
                  _statusBadge(),
                ],
              ]),
            ),
          ),
        ),
        const Divider(height: 1),
        if (_error != null)
          Padding(
            padding: EdgeInsets.fromLTRB(
                narrow ? 14 : 28, 14, narrow ? 14 : 28, 0),
            child: Center(
              child: ConstrainedBox(
                constraints:
                    const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
                child: _Notice(
                  kind: _NoticeKind.bad,
                  text: _error!,
                  action: TextButton(
                    onPressed: () => setState(() => _error = null),
                    child: const Text('忽略'),
                  ),
                ),
              ),
            ),
          ),
        Expanded(
          child: switch (_page) {
            _AppPage.connect => _connectPage(),
            _AppPage.traffic => _trafficPage(),
            _AppPage.nodes => _nodesPage(),
            _AppPage.region => _regionPage(),
            _AppPage.checks => _checksPage(),
            _AppPage.subscription => _subscriptionPage(),
          },
        ),
      ]);
  }

  Widget _statusBadge() {
    final (label, color, tint) =
        switch ((_connected, _blocked, _protectedNow)) {
      (false, _, _) => ('未连接', Tokens.inkMuted, Tokens.idleTint),
      (true, true, _) => ('已阻断', Tokens.bad, Tokens.badTint),
      (true, false, true) => ('已连接', Tokens.ok, Tokens.okTint),
      (true, false, false) => ('代理已关闭', Tokens.warn, Tokens.warnTint),
    };
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 7),
      decoration: BoxDecoration(
          color: tint, borderRadius: BorderRadius.circular(Tokens.radiusBadge)),
      child: Row(mainAxisSize: MainAxisSize.min, children: [
        _StatusDot(color: color, size: 7),
        const SizedBox(width: 8),
        Text(label,
            style: TextStyle(
                fontSize: 12, fontWeight: FontWeight.w600, color: color)),
      ]),
    );
  }
}

/// Pastes one node link and adds it. The local service owns the parsing — it is
/// the side that knows what the kernel accepts — so this dialog only carries the
/// text and shows what came back: the node it turned into, or the reason it was
/// refused.
class _AddNodeDialog extends StatefulWidget {
  const _AddNodeDialog({required this.request});

  /// Parses one link, or stores it when [commit] is true.
  final Future<(ManualNode?, String?)> Function(
      String uri, String name, bool commit) request;

  @override
  State<_AddNodeDialog> createState() => _AddNodeDialogState();
}

class _AddNodeDialogState extends State<_AddNodeDialog> {
  final _uri = TextEditingController();
  final _name = TextEditingController();
  ManualNode? _parsed;
  String _error = '';
  bool _busy = false;

  @override
  void dispose() {
    _uri.dispose();
    _name.dispose();
    super.dispose();
  }

  Future<void> _run({required bool commit}) async {
    setState(() {
      _busy = true;
      _error = '';
    });
    final (node, error) = await widget.request(_uri.text, _name.text, commit);
    if (!mounted) return;
    setState(() {
      _busy = false;
      _error = error ?? '';
      if (node != null) {
        _parsed = node;
      }
    });
    if (commit && node != null) {
      Navigator.of(context).pop(node);
    }
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
        title: const Text('添加节点'),
        content: SizedBox(
          width: dialogWidth(context, 460),
          child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextField(
                  controller: _uri,
                  autofocus: true,
                  maxLines: 3,
                  minLines: 1,
                  enabled: !_busy,
                  decoration: const InputDecoration(
                    labelText: '节点链接',
                    hintText:
                        'vmess:// / vless:// / trojan:// / ss:// / ssr:// / hysteria2:// / tuic://',
                    prefixIcon: Icon(Icons.link_rounded, size: 18),
                  ),
                ),
                const SizedBox(height: 12),
                TextField(
                  controller: _name,
                  enabled: !_busy,
                  decoration: const InputDecoration(
                    labelText: '名称（留空则用链接里的名字）',
                    prefixIcon: Icon(Icons.label_outline_rounded, size: 18),
                  ),
                ),
                const SizedBox(height: 14),
                if (_error.isNotEmpty)
                  _Notice(kind: _NoticeKind.bad, text: _error)
                else if (_parsed != null)
                  Text(parsedNodeLabel(_parsed!),
                      style: const TextStyle(fontSize: 12.5))
                else
                  const Text('解析结果会显示在这里。添加的节点与订阅节点一样参与测速、地区验证与自动切换。',
                      style: Tokens.hint),
              ]),
        ),
        actions: [
          TextButton(
              onPressed: _busy ? null : () => Navigator.of(context).pop(),
              child: const Text('取消')),
          TextButton(
              onPressed: _busy ? null : () => unawaited(_run(commit: false)),
              child: const Text('解析')),
          FilledButton(
              onPressed: _busy ? null : () => unawaited(_run(commit: true)),
              child: Text(_busy ? '处理中…' : '添加')),
        ],
      );
}

class _AkHoverNavItem extends StatefulWidget {
  const _AkHoverNavItem({
    required this.page,
    required this.selected,
    required this.count,
    required this.onTap,
  });

  final _AppPage page;
  final bool selected;
  final String? count;
  final VoidCallback onTap;

  @override
  State<_AkHoverNavItem> createState() => _AkHoverNavItemState();
}

class _AkHoverNavItemState extends State<_AkHoverNavItem> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final active = widget.selected || _hovered;
    final activeColor = widget.selected
        ? Tokens.cyan
        : (_hovered ? Tokens.cyanNeon : Tokens.inkMuted);
    final watermarkText = switch (widget.page) {
      _AppPage.connect => 'CONNECT',
      _AppPage.traffic => 'TRAFFIC',
      _AppPage.nodes => 'NODES',
      _AppPage.region => 'REGIONS',
      _AppPage.checks => 'DIAGNOSE',
      _AppPage.subscription => 'PROFILES',
    };

    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
      child: InkWell(
        onTap: widget.onTap,
        onHover: (hovered) {
          if (_hovered != hovered) setState(() => _hovered = hovered);
        },
        borderRadius: BorderRadius.circular(Tokens.radiusBadge),
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 200),
          curve: Curves.easeOutCubic,
          clipBehavior: Clip.antiAlias,
          decoration: BoxDecoration(
            color: widget.selected
                ? Tokens.cyanTint
                : (_hovered
                    ? Tokens.cyan.withOpacity(0.08)
                    : Colors.transparent),
            border: Border(
              left: BorderSide(
                color: widget.selected
                    ? Tokens.cyan
                    : (_hovered
                        ? Tokens.cyan.withOpacity(0.8)
                        : Colors.transparent),
                width: Tokens.barWidth,
              ),
              bottom: BorderSide(
                color: active
                    ? Tokens.cyan.withOpacity(0.22)
                    : Tokens.hairline.withOpacity(0.3),
                width: 0.8,
              ),
            ),
          ),
          child: Stack(
            children: [
              // Arknights giant watermark text in background (smooth fade & slide on hover)
              Positioned(
                right: -6,
                bottom: -8,
                child: IgnorePointer(
                  child: AnimatedOpacity(
                    duration: const Duration(milliseconds: 240),
                    opacity: active ? (widget.selected ? 0.18 : 0.12) : 0.0,
                    child: AnimatedSlide(
                      duration: const Duration(milliseconds: 240),
                      curve: Curves.easeOutCubic,
                      offset: active ? Offset.zero : const Offset(0.15, 0),
                      child: Text(
                        watermarkText,
                        style: TextStyle(
                          fontFamily: 'Bender',
                          fontFamilyFallback: Tokens.fontFamilyFallback,
                          fontSize: 24,
                          fontWeight: FontWeight.w900,
                          letterSpacing: 2.0,
                          color: Tokens.cyan,
                        ),
                      ),
                    ),
                  ),
                ),
              ),

              // Foreground row
              Padding(
                padding: const EdgeInsets.fromLTRB(10, 9, 12, 9),
                child: Row(
                  children: [
                    // Floating glowing Rhodes Island Diamond indicator (◇)
                    AnimatedOpacity(
                      duration: const Duration(milliseconds: 180),
                      opacity: active ? 1.0 : 0.0,
                      child: Container(
                        margin: const EdgeInsets.only(right: 6),
                        child: Transform.rotate(
                          angle: 0.785398, // 45 degrees
                          child: Container(
                            width: 6,
                            height: 6,
                            decoration: BoxDecoration(
                              color: Tokens.cyan,
                              boxShadow: [
                                BoxShadow(
                                  color: Tokens.cyan.withOpacity(0.8),
                                  blurRadius: 6,
                                ),
                              ],
                            ),
                          ),
                        ),
                      ),
                    ),
                    Icon(widget.page.icon, size: 17, color: activeColor),
                    const SizedBox(width: 8),
                    Expanded(
                      child: AnimatedSlide(
                        duration: const Duration(milliseconds: 180),
                        curve: Curves.easeOutCubic,
                        offset: active ? const Offset(0.015, 0) : Offset.zero,
                        child: Row(
                          crossAxisAlignment: CrossAxisAlignment.baseline,
                          textBaseline: TextBaseline.alphabetic,
                          children: [
                            Text(
                              widget.page.label,
                              style: TextStyle(
                                fontSize: 13,
                                fontWeight:
                                    active ? FontWeight.w700 : FontWeight.w400,
                                color:
                                    widget.selected ? Tokens.cyan : Tokens.ink,
                                letterSpacing: 0.3,
                              ),
                            ),
                            const SizedBox(width: 6),
                            Flexible(
                              child: Text(
                                widget.page.englishCode,
                                overflow: TextOverflow.ellipsis,
                                style: Tokens.akBilingualEn.copyWith(
                                  fontSize: 8.5,
                                  color: active
                                      ? Tokens.cyan.withOpacity(0.85)
                                      : Tokens.inkFaint.withOpacity(0.6),
                                  fontWeight: active
                                      ? FontWeight.w700
                                      : FontWeight.w500,
                                ),
                              ),
                            ),
                          ],
                        ),
                      ),
                    ),
                    if (widget.count != null)
                      Container(
                        padding: const EdgeInsets.symmetric(
                            horizontal: 6, vertical: 2),
                        decoration: BoxDecoration(
                          color: active
                              ? Tokens.cyan.withOpacity(0.2)
                              : Tokens.surface,
                          borderRadius: BorderRadius.circular(2),
                          border: Border.all(
                            color: active
                                ? Tokens.cyan.withOpacity(0.5)
                                : Tokens.hairline,
                          ),
                        ),
                        child: Text(
                          widget.count!,
                          style: Tokens.number.copyWith(
                            fontSize: 11,
                            color: active ? Tokens.cyan : Tokens.inkMuted,
                          ),
                        ),
                      ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
