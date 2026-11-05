part of '../main.dart';

/// The scrolling body every sub-page uses: left aligned, capped width so
/// text lines stay readable on a wide window, with responsive margins.
class _PageBody extends StatelessWidget {
  const _PageBody({required this.children});

  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return SingleChildScrollView(
      padding: EdgeInsets.fromLTRB(
        narrow ? 14 : 28,
        narrow ? 12 : 20,
        narrow ? 14 : 28,
        32,
      ),
      child: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: children,
          ),
        ),
      ),
    );
  }
}

/// Tactical Rhodes Island Operations Panel.
/// Sharp chamfered geometry (45° cut corners) with technical borders,
/// optional top-right corner notch and left operational accent bar.
class _Panel extends StatelessWidget {
  const _Panel({
    required this.child,
    this.padding = const EdgeInsets.all(18),
    this.highlighted = false,
    this.showCorner = false,
    this.accentColor,
  });

  final Widget child;
  final EdgeInsets padding;
  final bool highlighted;
  final bool showCorner;
  final Color? accentColor;

  @override
  Widget build(BuildContext context) {
    final activeColor =
        accentColor ?? (highlighted ? Tokens.yellow : Tokens.cyan);
    const cutShape = BeveledRectangleBorder(
      borderRadius: BorderRadius.all(Radius.circular(Tokens.akCutMd)),
    );

    return ClipPath(
      clipper: const ShapeBorderClipper(shape: cutShape),
      child: CustomPaint(
        foregroundPainter: (highlighted || showCorner)
            ? _AkPanelAccentPainter(
                color: activeColor,
                drawCorner: highlighted || showCorner,
                drawBar: highlighted,
                cornerSize: Tokens.akCutMd + 3,
                barWidth: Tokens.barWidth,
              )
            : null,
        child: Container(
          padding: padding,
          decoration: ShapeDecoration(
            color: Tokens.surface,
            shape: BeveledRectangleBorder(
              borderRadius:
                  const BorderRadius.all(Radius.circular(Tokens.akCutMd)),
              side: BorderSide(
                color: highlighted
                    ? activeColor.withValues(alpha: 0.65)
                    : Tokens.hairline,
                width: 1.0,
              ),
            ),
            shadows: highlighted
                ? [
                    BoxShadow(
                      color: activeColor.withValues(alpha: 0.12),
                      blurRadius: 14,
                      offset: const Offset(0, 2),
                    ),
                  ]
                : null,
          ),
          child: Material(type: MaterialType.transparency, child: child),
        ),
      ),
    );
  }
}

/// Panel accents painter: draws the top-right corner indicator
/// and/or the left-side active accent bar.
class _AkPanelAccentPainter extends CustomPainter {
  const _AkPanelAccentPainter({
    required this.color,
    required this.drawCorner,
    required this.drawBar,
    this.cornerSize = 10.0,
    this.barWidth = 3.5,
  });

  final Color color;
  final bool drawCorner;
  final bool drawBar;
  final double cornerSize;
  final double barWidth;

  @override
  void paint(Canvas canvas, Size canvasSize) {
    final paint = Paint()
      ..color = color
      ..style = PaintingStyle.fill;

    if (drawBar) {
      canvas.drawRect(Rect.fromLTWH(0, 0, barWidth, canvasSize.height), paint);
    }

    if (drawCorner) {
      final path = Path()
        ..moveTo(canvasSize.width - cornerSize, 0)
        ..lineTo(canvasSize.width, 0)
        ..lineTo(canvasSize.width, cornerSize)
        ..close();
      canvas.drawPath(path, paint);
    }
  }

  @override
  bool shouldRepaint(covariant _AkPanelAccentPainter oldDelegate) =>
      oldDelegate.color != color ||
      oldDelegate.drawCorner != drawCorner ||
      oldDelegate.drawBar != drawBar ||
      oldDelegate.cornerSize != cornerSize ||
      oldDelegate.barWidth != barWidth;
}

/// Tactical section header with 3.5px accent bar, Chinese primary title,
/// short English secondary code, and optional trailing controls.
class _SectionHeader extends StatelessWidget {
  const _SectionHeader({
    required this.title,
    this.subtitle,
    this.trailing,
  }) : accentColor = null;

  final String title;
  final String? subtitle;
  final Widget? trailing;
  final Color? accentColor;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(builder: (context, constraints) {
      return Padding(
        padding: const EdgeInsets.only(bottom: 12),
        child: constraints.maxWidth < 760 && trailing != null
            ? Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                _buildTitle(),
                const SizedBox(height: 12),
                trailing!,
              ])
            : Row(children: [
                Expanded(child: _buildTitle()),
                if (trailing != null) trailing!,
              ]),
      );
    });
  }

  Widget _buildTitle() => Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Text(title, style: Tokens.section),
          if (subtitle != null && subtitle!.isNotEmpty)
            Text(subtitle!,
                style: Tokens.akBilingualEn.copyWith(
                  color: accentColor ?? Tokens.yellow,
                )),
        ],
      );
}

/// Tactical 45° diagonal warning / technical stripes.
class _AkStripes extends StatelessWidget {
  const _AkStripes({
    this.height = 3.0,
    this.color = Tokens.warn,
    this.background = const Color(0x00000000),
  }) : stripeWidth = 5.0;

  final double height;
  final Color color;
  final Color background;
  final double stripeWidth;

  @override
  Widget build(BuildContext context) => SizedBox(
        height: height,
        child: CustomPaint(
          size: Size.infinite,
          painter: _StripesPainter(
            color: color,
            background: background,
            stripeWidth: stripeWidth,
          ),
        ),
      );
}

class _StripesPainter extends CustomPainter {
  const _StripesPainter({
    required this.color,
    required this.background,
    required this.stripeWidth,
  });

  final Color color;
  final Color background;
  final double stripeWidth;

  @override
  void paint(Canvas canvas, Size size) {
    if (background.a > 0) {
      canvas.drawRect(Rect.fromLTWH(0, 0, size.width, size.height),
          Paint()..color = background);
    }
    final paint = Paint()
      ..color = color
      ..style = PaintingStyle.fill;
    canvas.save();
    canvas.clipRect(Rect.fromLTWH(0, 0, size.width, size.height));
    final period = stripeWidth * 2;
    for (double x = -size.height; x < size.width + size.height; x += period) {
      final path = Path()
        ..moveTo(x, 0)
        ..lineTo(x + stripeWidth, 0)
        ..lineTo(x + stripeWidth - size.height, size.height)
        ..lineTo(x - size.height, size.height)
        ..close();
      canvas.drawPath(path, paint);
    }
    canvas.restore();
  }

  @override
  bool shouldRepaint(covariant _StripesPainter oldDelegate) =>
      oldDelegate.color != color ||
      oldDelegate.background != background ||
      oldDelegate.stripeWidth != stripeWidth;
}

/// Tactical list row with diamond indicator, left accent bar, and clean hover.
class _AkHoverRow extends StatefulWidget {
  const _AkHoverRow({
    required this.child,
    this.onTap,
    this.selected = false,
    this.accentColor = Tokens.yellow,
    this.padding = const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
  })  : watermark = null,
        previewTag = null,
        showDivider = true;

  final Widget child;
  final VoidCallback? onTap;
  final bool selected;
  final String? watermark;
  final String? previewTag;
  final Color accentColor;
  final bool showDivider;
  final EdgeInsets padding;

  @override
  State<_AkHoverRow> createState() => _AkHoverRowState();
}

class _AkHoverRowState extends State<_AkHoverRow> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final active = widget.selected || _hovered;
    final accent = widget.accentColor;

    Widget content = AnimatedContainer(
      duration: const Duration(milliseconds: 160),
      curve: Curves.easeOutCubic,
      decoration: BoxDecoration(
        color: widget.selected
            ? accent.withValues(alpha: 0.09)
            : (_hovered ? accent.withValues(alpha: 0.04) : Colors.transparent),
        border: Border(
          left: BorderSide(
            color: widget.selected
                ? accent
                : (_hovered
                    ? accent.withValues(alpha: 0.8)
                    : Colors.transparent),
            width: Tokens.barWidth,
          ),
          bottom: widget.showDivider
              ? BorderSide(
                  color: active
                      ? accent.withValues(alpha: 0.25)
                      : Tokens.hairline.withValues(alpha: 0.4),
                  width: 0.8,
                )
              : BorderSide.none,
        ),
      ),
      child: Padding(
        padding: widget.padding,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.center,
          children: [
            // Glowing Rhodes Island diamond (◇)
            AnimatedOpacity(
              duration: const Duration(milliseconds: 150),
              opacity: active ? 1.0 : 0.0,
              child: Container(
                margin: const EdgeInsets.only(right: 8),
                child: Transform.rotate(
                  angle: 0.785398,
                  child: Container(
                    width: 6,
                    height: 6,
                    decoration: BoxDecoration(
                      color: accent,
                      boxShadow: [
                        BoxShadow(
                          color: accent.withValues(alpha: 0.8),
                          blurRadius: 5,
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ),
            Expanded(child: widget.child),
          ],
        ),
      ),
    );

    if (widget.onTap != null) {
      return InkWell(
        onTap: widget.onTap,
        onHover: (hovered) {
          if (_hovered != hovered) setState(() => _hovered = hovered);
        },
        child: content,
      );
    }

    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: content,
    );
  }
}

/// Tactical interactive icon button with min 48px touch target.
class _AkHoverIconButton extends StatefulWidget {
  const _AkHoverIconButton({
    required this.icon,
    required this.onPressed,
    this.tooltip,
    this.color,
    this.size = 18.0,
    this.padding = const EdgeInsets.all(10),
  }) : previewCode = null;

  final Widget icon;
  final VoidCallback? onPressed;
  final String? tooltip;
  final String? previewCode;
  final Color? color;
  final double size;
  final EdgeInsets padding;

  @override
  State<_AkHoverIconButton> createState() => _AkHoverIconButtonState();
}

class _AkHoverIconButtonState extends State<_AkHoverIconButton> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final enabled = widget.onPressed != null;
    final accent = widget.color ?? Tokens.yellow;

    Widget button = MouseRegion(
      onEnter: enabled ? (_) => setState(() => _hovered = true) : null,
      onExit: enabled ? (_) => setState(() => _hovered = false) : null,
      cursor: enabled ? SystemMouseCursors.click : SystemMouseCursors.basic,
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: widget.onPressed,
        child: ConstrainedBox(
          constraints: const BoxConstraints(minWidth: 44, minHeight: 44),
          child: Center(
            child: AnimatedContainer(
              duration: const Duration(milliseconds: 160),
              padding: widget.padding,
              decoration: BoxDecoration(
                color: _hovered && enabled
                    ? accent.withValues(alpha: 0.12)
                    : Colors.transparent,
                border: Border.all(
                  color: _hovered && enabled
                      ? accent.withValues(alpha: 0.7)
                      : Colors.transparent,
                  width: 1.0,
                ),
              ),
              child: widget.icon,
            ),
          ),
        ),
      ),
    );

    if (widget.tooltip != null) {
      return Tooltip(
        message: widget.previewCode != null
            ? '// ${widget.previewCode!} · ${widget.tooltip!}'
            : widget.tooltip!,
        child: button,
      );
    }
    return button;
  }
}

/// Button hover wrapper with tactical outline glow.
class _Badge extends StatelessWidget {
  const _Badge({
    required this.label,
    required this.color,
    this.tint,
    this.showDot = false,
  });

  final String label;
  final Color color;
  final Color? tint;
  final bool showDot;

  @override
  Widget build(BuildContext context) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2.5),
        decoration: ShapeDecoration(
          color: tint ?? Tokens.idleTint,
          shape: BeveledRectangleBorder(
            borderRadius:
                const BorderRadius.all(Radius.circular(Tokens.akCutSm)),
            side: BorderSide(color: color.withValues(alpha: 0.4), width: 0.9),
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (showDot) ...[
              Transform.rotate(
                angle: 0.785398,
                child: Container(
                  width: 4.5,
                  height: 4.5,
                  color: color,
                ),
              ),
              const SizedBox(width: 5),
            ],
            Text(
              label,
              style: TextStyle(
                fontFamily: 'JetBrains Mono',
                fontFamilyFallback: Tokens.fontMonoFallback,
                fontSize: 11,
                fontWeight: FontWeight.w700,
                color: color,
                letterSpacing: 0.3,
              ),
            ),
          ],
        ),
      );
}

/// Status indicator dot with glow.
class _StatusDot extends StatelessWidget {
  const _StatusDot({required this.color, this.size = 8});

  final Color color;
  final double size;

  @override
  Widget build(BuildContext context) => Container(
        width: size,
        height: size,
        decoration: BoxDecoration(
          color: color,
          shape: BoxShape.circle,
          boxShadow: [
            BoxShadow(color: color.withValues(alpha: 0.6), blurRadius: 5),
          ],
        ),
      );
}

enum _NoticeKind { ok, warn, bad, info }

/// Operational Notice / Warning Banner with technical stripes and left accent bar.
class _Notice extends StatelessWidget {
  const _Notice({required this.text, required this.kind, this.action});

  final String text;
  final _NoticeKind kind;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final (color, tint, icon) = switch (kind) {
      _NoticeKind.ok => (Tokens.ok, Tokens.okTint, Icons.check_circle_rounded),
      _NoticeKind.warn => (
          Tokens.warn,
          Tokens.warnTint,
          Icons.warning_amber_rounded
        ),
      _NoticeKind.bad => (
          Tokens.bad,
          Tokens.badTint,
          Icons.error_outline_rounded
        ),
      _NoticeKind.info => (
          Tokens.cyan,
          Tokens.cyanTint,
          Icons.info_outline_rounded
        ),
    };
    final isAlert = kind == _NoticeKind.warn || kind == _NoticeKind.bad;
    return ClipRRect(
      borderRadius: BorderRadius.circular(Tokens.radiusControl),
      child: Container(
        decoration: BoxDecoration(
          color: tint,
          border: Border(
            top: BorderSide(color: color.withValues(alpha: 0.35), width: 1),
            right: BorderSide(color: color.withValues(alpha: 0.35), width: 1),
            bottom: BorderSide(color: color.withValues(alpha: 0.35), width: 1),
            left: BorderSide(color: color, width: Tokens.barWidth),
          ),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (isAlert)
              _AkStripes(
                height: 2.5,
                color: color.withValues(alpha: 0.5),
                background: Colors.transparent,
              ),
            Padding(
              padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(icon, size: 18, color: color),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          text,
                          style: TextStyle(
                            color: color,
                            fontSize: 12.5,
                            height: 1.45,
                            fontWeight: FontWeight.w600,
                          ),
                        ),
                        if (action != null)
                          Padding(
                            padding: const EdgeInsets.only(top: 4),
                            child: Align(
                              alignment: Alignment.centerLeft,
                              child: action!,
                            ),
                          ),
                      ],
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// Actionable Empty State with Rhodes Island terminal styling.
class _EmptyState extends StatelessWidget {
  const _EmptyState({
    required this.text,
    this.actionLabel,
    this.onAction,
    this.secondaryActionLabel,
    this.onSecondaryAction,
    this.illustration,
  });

  final String text;
  final String? actionLabel;
  final VoidCallback? onAction;
  final String? secondaryActionLabel;
  final VoidCallback? onSecondaryAction;
  final Widget? illustration;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 24, horizontal: 16),
        child: Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              if (illustration != null)
                illustration!
              else
                const Icon(Icons.inbox_outlined,
                    size: 28, color: Tokens.inkFaint),
              const SizedBox(height: 12),
              Text(
                text,
                textAlign: TextAlign.center,
                style: Tokens.body.copyWith(color: Tokens.inkMuted),
              ),
              if (actionLabel != null || secondaryActionLabel != null) ...[
                const SizedBox(height: 16),
                Wrap(
                  spacing: 12,
                  runSpacing: 10,
                  alignment: WrapAlignment.center,
                  children: [
                    if (actionLabel != null)
                      FilledButton(
                        onPressed: onAction,
                        child: Text(actionLabel!),
                      ),
                    if (secondaryActionLabel != null)
                      OutlinedButton(
                        onPressed: onSecondaryAction,
                        child: Text(secondaryActionLabel!),
                      ),
                  ],
                ),
              ],
            ],
          ),
        ),
      );
}

/// A readout line: label in muted type, value in tabular figures.
class _Stats extends StatelessWidget {
  const _Stats({required this.items});

  final List<(String, String)> items;

  @override
  Widget build(BuildContext context) => Wrap(
        spacing: 24,
        runSpacing: 12,
        children: [
          for (final (label, value) in items)
            Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(label, style: Tokens.hint),
              const SizedBox(height: 3),
              Text(value, style: Tokens.number),
            ]),
        ],
      );
}

/// Tactical Key-Value Row with technical formatting.
class _KeyValueRow extends StatelessWidget {
  const _KeyValueRow({
    required this.label,
    required this.value,
    this.trailing,
    this.valueColor,
  });

  final String label;
  final String value;
  final Widget? trailing;
  final Color? valueColor;

  @override
  Widget build(BuildContext context) {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 5),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: narrow ? 100 : 130,
            child: Text(label, style: Tokens.hint),
          ),
          Expanded(
            child: Text(
              value,
              style: Tokens.number.copyWith(
                fontWeight: FontWeight.w500,
                color: valueColor ?? Tokens.ink,
              ),
            ),
          ),
          if (trailing != null) trailing!,
        ],
      ),
    );
  }
}

double dialogWidth(BuildContext context, double designed) =>
    MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint
        ? double.maxFinite
        : designed;

/// Redesigned Route Matrix Card:
/// Real path: 本机 ──◇── 节点 ──◇── 出口地区与 IP.
class _PathSurface extends StatelessWidget {
  const _PathSurface({
    required this.stateLabel,
    required this.nodeLabel,
    required this.regionLabel,
    required this.connected,
    required this.stats,
    this.onTapNode,
  });

  final String stateLabel;
  final String nodeLabel;
  final String regionLabel;
  final bool connected;
  final List<(String, String)> stats;
  final VoidCallback? onTapNode;

  @override
  Widget build(BuildContext context) {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return _Panel(
      highlighted: connected,
      showCorner: true,
      accentColor: connected ? Tokens.ok : Tokens.yellow,
      padding: const EdgeInsets.fromLTRB(18, 16, 18, 16),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          _StatusDot(color: connected ? Tokens.ok : Tokens.inkFaint, size: 8),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              stateLabel,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(
                fontSize: 13.5,
                fontWeight: FontWeight.w700,
                letterSpacing: 0.3,
              ),
            ),
          ),
          Text('// ROUTING_MATRIX',
              style: Tokens.akBilingualEn.copyWith(fontSize: 9.5)),
        ]),
        const SizedBox(height: 14),
        if (narrow)
          // Mobile stack layout
          Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              _buildOriginBadge(),
              const SizedBox(width: 8),
              const Icon(Icons.arrow_right_alt_rounded,
                  size: 16, color: Tokens.inkFaint),
              const SizedBox(width: 8),
              Expanded(child: _buildNodeCard(connected)),
            ]),
            const SizedBox(height: 8),
            Row(children: [
              const SizedBox(width: 48),
              const Icon(Icons.subdirectory_arrow_right_rounded,
                  size: 16, color: Tokens.inkFaint),
              const SizedBox(width: 8),
              _buildExitBadge(connected),
            ]),
          ])
        else
          // Desktop wide layout
          Row(children: [
            _buildOriginBadge(),
            const SizedBox(width: 8),
            _Connector(active: connected),
            const SizedBox(width: 8),
            Flexible(child: _buildNodeCard(connected)),
            const SizedBox(width: 8),
            _Connector(active: connected),
            const SizedBox(width: 8),
            _buildExitBadge(connected),
          ]),
        const SizedBox(height: 14),
        const Divider(),
        const SizedBox(height: 12),
        _Stats(items: stats),
      ]),
    );
  }

  Widget _buildOriginBadge() => Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
        decoration: const ShapeDecoration(
          color: Tokens.surfaceElevated,
          shape: BeveledRectangleBorder(
            borderRadius: BorderRadius.all(Radius.circular(Tokens.akCutSm)),
            side: BorderSide(color: Tokens.hairlineBright, width: 0.9),
          ),
        ),
        child: const Text('本机',
            style: TextStyle(fontSize: 12, fontWeight: FontWeight.w700)),
      );

  Widget _buildNodeCard(bool isConnected) => InkWell(
        onTap: onTapNode,
        borderRadius: BorderRadius.circular(Tokens.radiusBadge),
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
          decoration: ShapeDecoration(
            color: isConnected ? Tokens.cyanTint : Tokens.surfaceElevated,
            shape: BeveledRectangleBorder(
              borderRadius:
                  const BorderRadius.all(Radius.circular(Tokens.akCutSm)),
              side: BorderSide(
                color: isConnected
                    ? Tokens.cyan.withValues(alpha: 0.65)
                    : Tokens.hairlineBright,
                width: 0.9,
              ),
            ),
          ),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Flexible(
                child: Text(
                  nodeLabel,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontFamily: 'JetBrains Mono',
                    fontFamilyFallback: Tokens.fontMonoFallback,
                    fontSize: 12.5,
                    fontWeight: FontWeight.w700,
                    color: isConnected ? Tokens.cyan : Tokens.ink,
                  ),
                ),
              ),
              if (onTapNode != null) ...[
                const SizedBox(width: 6),
                const Icon(Icons.swap_horiz_rounded,
                    size: 14, color: Tokens.inkMuted),
              ],
            ],
          ),
        ),
      );

  Widget _buildExitBadge(bool isConnected) => _Badge(
        label: regionLabel,
        color: isConnected ? Tokens.cyan : Tokens.inkMuted,
        tint: isConnected ? Tokens.cyanTint : Tokens.idleTint,
        showDot: true,
      );
}

class _Connector extends StatelessWidget {
  const _Connector({required this.active});

  final bool active;

  @override
  Widget build(BuildContext context) => Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Container(
            width: 14,
            height: 1.5,
            color:
                active ? Tokens.cyan.withValues(alpha: 0.6) : Tokens.hairline,
          ),
          Transform.rotate(
            angle: 0.785398,
            child: Container(
              width: 5.5,
              height: 5.5,
              color: active ? Tokens.cyan : Tokens.inkFaint,
            ),
          ),
          Container(
            width: 14,
            height: 1.5,
            color:
                active ? Tokens.cyan.withValues(alpha: 0.6) : Tokens.hairline,
          ),
        ],
      );
}

/// Hero Decisive Tactical Connect Control for the Overview Page.
class _PowerSwitchHero extends StatelessWidget {
  const _PowerSwitchHero({
    required this.connected,
    required this.busy,
    required this.port,
    required this.latency,
    required this.onToggle,
    required this.onMeasure,
    required this.statusDetail,
  });

  final bool connected;
  final bool busy;
  final int? port;
  final int? latency;
  final VoidCallback onToggle;
  final VoidCallback onMeasure;
  final String statusDetail;

  @override
  Widget build(BuildContext context) => _Panel(
        showCorner: true,
        accentColor: Tokens.yellow,
        padding: const EdgeInsets.all(24),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text('01 / NETWORK CONTROL',
              style: Tokens.akBilingualEn.copyWith(color: Tokens.yellow)),
          const SizedBox(height: 16),
          Text(busy ? '正在处理' : (connected ? '连接管理' : '建立连接'),
              style: const TextStyle(
                  fontSize: 30, fontWeight: FontWeight.w800, letterSpacing: 2)),
          const SizedBox(height: 10),
          Text(statusDetail, style: Tokens.small),
          const SizedBox(height: 24),
          Wrap(spacing: 10, runSpacing: 10, children: [
            FilledButton.icon(
              onPressed: busy || port == null ? null : onToggle,
              icon: Icon(
                  connected ? Icons.stop_rounded : Icons.arrow_forward_rounded),
              label: Text(busy ? '正在处理…' : (connected ? '断开代理连接' : '启动代理连接')),
            ),
            OutlinedButton.icon(
              onPressed: busy || !connected ? null : onMeasure,
              icon: const Icon(Icons.speed_rounded, size: 18),
              label: Text(latency == null ? '测试延迟' : '$latency ms'),
            ),
          ]),
        ]),
      );
}

/// Tactical Mode Selection Chip.
class _ModeChip extends StatelessWidget {
  const _ModeChip({
    required this.label,
    required this.selected,
    required this.onTap,
  });

  final String label;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(Tokens.radiusBadge),
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 6),
          decoration: BoxDecoration(
            color: selected ? Tokens.yellowTint : Tokens.surface,
            border: Border.all(
              color: selected ? Tokens.yellow : Tokens.hairline,
              width: 1.0,
            ),
            borderRadius: BorderRadius.circular(Tokens.radiusBadge),
          ),
          child: Text(
            label,
            style: TextStyle(
              fontSize: 12.5,
              fontWeight: FontWeight.w600,
              color: selected ? Tokens.yellow : Tokens.inkMuted,
            ),
          ),
        ),
      );
}

/// Titled group of label lines used by diagnostic views.
class _DiagnosticSection extends StatelessWidget {
  const _DiagnosticSection(this.title, this.values);

  final String title;
  final List<String> values;

  @override
  Widget build(BuildContext context) => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(title, style: Tokens.section),
          const SizedBox(height: 6),
          for (final value in values)
            Padding(
              padding: const EdgeInsets.only(top: 3),
              child: Text(value, style: const TextStyle(height: 1.4)),
            ),
        ],
      );
}

/// Tactile Jelly Tap wrapper for backward-compatibility.
