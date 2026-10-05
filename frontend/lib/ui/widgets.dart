part of '../main.dart';

/// Tactile Jelly Tap wrapper: scales down on press, springs back with elastic bounce on release.
class _JellyTap extends StatefulWidget {
  const _JellyTap({
    super.key,
    required this.child,
    this.onTap,
    this.enabled = true,
    this.scaleDown = 0.94,
  });

  final Widget child;
  final VoidCallback? onTap;
  final bool enabled;
  final double scaleDown;

  @override
  State<_JellyTap> createState() => _JellyTapState();
}

class _JellyTapState extends State<_JellyTap> with SingleTickerProviderStateMixin {
  late final AnimationController _controller;
  late final Animation<double> _scaleAnimation;

  @override
  void initState() {
    super.initState();
    _controller = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 320),
      value: 1.0,
    );
    _scaleAnimation = Tween<double>(begin: widget.scaleDown, end: 1.0).animate(
      CurvedAnimation(parent: _controller, curve: Curves.elasticOut),
    );
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  void _onTapDown(TapDownDetails _) {
    if (widget.enabled && widget.onTap != null) {
      _controller.value = 0.0;
    }
  }

  void _onTapUp(TapUpDetails _) {
    if (widget.enabled && widget.onTap != null) {
      _controller.forward(from: 0.0);
      widget.onTap!();
    }
  }

  void _onTapCancel() {
    if (widget.enabled && widget.onTap != null) {
      _controller.forward(from: 0.0);
    }
  }

  @override
  Widget build(BuildContext context) {
    if (!widget.enabled || widget.onTap == null) {
      return widget.child;
    }
    return GestureDetector(
      behavior: HitTestBehavior.opaque,
      onTapDown: _onTapDown,
      onTapUp: _onTapUp,
      onTapCancel: _onTapCancel,
      child: AnimatedBuilder(
        animation: _scaleAnimation,
        builder: (context, child) => Transform.scale(
          scale: _scaleAnimation.value,
          child: child,
        ),
        child: widget.child,
      ),
    );
  }
}

/// Dynamic Concentric Ripple Wave Aura: emits continuous expanding rings when active or busy.
class _RippleWave extends StatefulWidget {
  const _RippleWave({
    super.key,
    required this.active,
    required this.color,
    this.size = 130.0,
  });

  final bool active;
  final Color color;
  final double size;

  @override
  State<_RippleWave> createState() => _RippleWaveState();
}

class _RippleWaveState extends State<_RippleWave> with SingleTickerProviderStateMixin {
  late final AnimationController _controller;

  @override
  void initState() {
    super.initState();
    _controller = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 2200),
    );
    if (widget.active) {
      _controller.repeat();
    }
  }

  @override
  void didUpdateWidget(covariant _RippleWave oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.active != oldWidget.active) {
      if (widget.active) {
        _controller.repeat();
      } else {
        _controller.stop();
        _controller.value = 0.0;
      }
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    if (!widget.active) {
      return SizedBox(
        width: widget.size,
        height: widget.size,
        child: Center(
          child: Container(
            width: widget.size * 0.85,
            height: widget.size * 0.85,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              border: Border.all(color: widget.color.withOpacity(0.18), width: 1.5),
            ),
          ),
        ),
      );
    }
    return AnimatedBuilder(
      animation: _controller,
      builder: (context, _) => SizedBox(
        width: widget.size,
        height: widget.size,
        child: CustomPaint(
          painter: _WaveRipplePainter(
            t: _controller.value,
            color: widget.color,
          ),
        ),
      ),
    );
  }
}

class _WaveRipplePainter extends CustomPainter {
  _WaveRipplePainter({required this.t, required this.color});
  final double t;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final center = Offset(size.width / 2, size.height / 2);
    final maxRadius = size.width / 2;

    for (int i = 0; i < 3; i++) {
      final phase = (t + (i * 0.33)) % 1.0;
      final radius = maxRadius * (0.55 + 0.45 * phase);
      final alpha = (1.0 - phase) * 0.45;
      final paint = Paint()
        ..color = color.withOpacity(alpha.clamp(0.0, 1.0))
        ..style = PaintingStyle.stroke
        ..strokeWidth = 2.0 - (0.8 * phase);
      canvas.drawCircle(center, radius, paint);
    }
  }

  @override
  bool shouldRepaint(covariant _WaveRipplePainter oldDelegate) =>
      oldDelegate.t != t || oldDelegate.color != color;
}

/// The scrolling body every simple sub-page uses: left aligned, capped width so
/// text lines stay readable on a wide window.
class _PageBody extends StatelessWidget {
  const _PageBody({required this.children});

  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return SingleChildScrollView(
      padding: EdgeInsets.fromLTRB(
          narrow ? 14 : 28,
          narrow ? 12 : 22,
          narrow ? 14 : 28,
          32),
      child: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: Tokens.contentMaxWidth),
          child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: children),
        ),
      ),
    );
  }
}

/// AKDS .ak-corner: Authentic PRTS 45° corner triangle accent.
class _AkCornerPainter extends CustomPainter {
  const _AkCornerPainter({
    required this.color,
    this.size = 9.0,
    this.topRight = true,
  });

  final Color color;
  final double size;
  final bool topRight;

  @override
  void paint(Canvas canvas, Size canvasSize) {
    final paint = Paint()
      ..color = color
      ..style = PaintingStyle.fill;
    final path = Path();
    if (topRight) {
      path.moveTo(canvasSize.width - size, 0);
      path.lineTo(canvasSize.width, 0);
      path.lineTo(canvasSize.width, size);
    } else {
      path.moveTo(0, 0);
      path.lineTo(size, 0);
      path.lineTo(0, size);
    }
    path.close();
    canvas.drawPath(path, paint);
  }

  @override
  bool shouldRepaint(covariant _AkCornerPainter oldDelegate) =>
      oldDelegate.color != color ||
      oldDelegate.size != size ||
      oldDelegate.topRight != topRight;
}

/// AKDS .ak-stripes: 45-degree diagonal warning / hazard / technical stripes.
class _AkStripes extends StatelessWidget {
  const _AkStripes({
    super.key,
    this.height = 3.5,
    this.color = Tokens.warn,
    this.background = const Color(0x00000000),
    this.stripeWidth = 5.0,
  });

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
    if (background.alpha > 0) {
      canvas.drawRect(
          Rect.fromLTWH(0, 0, size.width, size.height),
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

/// AKDS .ak-brackets: Precision HUD technical framing brackets at 4 corners.
class _AkBrackets extends StatelessWidget {
  const _AkBrackets({
    super.key,
    required this.child,
    this.color = Tokens.hairlineBright,
    this.length = 10.0,
    this.strokeWidth = 1.2,
  });

  final Widget child;
  final Color color;
  final double length;
  final double strokeWidth;

  @override
  Widget build(BuildContext context) => CustomPaint(
        foregroundPainter: _BracketsPainter(
          color: color,
          length: length,
          strokeWidth: strokeWidth,
        ),
        child: child,
      );
}

class _BracketsPainter extends CustomPainter {
  const _BracketsPainter({
    required this.color,
    required this.length,
    required this.strokeWidth,
  });

  final Color color;
  final double length;
  final double strokeWidth;

  @override
  void paint(Canvas canvas, Size size) {
    final paint = Paint()
      ..color = color
      ..strokeWidth = strokeWidth
      ..style = PaintingStyle.stroke;

    // Top-left
    canvas.drawLine(const Offset(0, 0), Offset(length, 0), paint);
    canvas.drawLine(const Offset(0, 0), Offset(0, length), paint);

    // Top-right
    canvas.drawLine(Offset(size.width, 0), Offset(size.width - length, 0), paint);
    canvas.drawLine(Offset(size.width, 0), Offset(size.width, length), paint);

    // Bottom-left
    canvas.drawLine(Offset(0, size.height), Offset(length, size.height), paint);
    canvas.drawLine(Offset(0, size.height), Offset(0, size.height - length), paint);

    // Bottom-right
    canvas.drawLine(Offset(size.width, size.height), Offset(size.width - length, size.height), paint);
    canvas.drawLine(Offset(size.width, size.height), Offset(size.width, size.height - length), paint);
  }

  @override
  bool shouldRepaint(covariant _BracketsPainter oldDelegate) =>
      oldDelegate.color != color ||
      oldDelegate.length != length ||
      oldDelegate.strokeWidth != strokeWidth;
}

/// The content surface. An AKDS industrial panel with sharp geometry and glowing highlight.
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
    final activeColor = accentColor ?? Tokens.cyan;
    return ClipRRect(
      borderRadius: BorderRadius.circular(Tokens.radiusPanel),
      child: CustomPaint(
        foregroundPainter: (highlighted || showCorner)
            ? _AkPanelAccentPainter(
                color: activeColor,
                drawCorner: highlighted || showCorner,
                drawBar: highlighted,
                cornerSize: 10,
                barWidth: Tokens.barWidth,
              )
            : null,
        child: Container(
          padding: padding,
          decoration: BoxDecoration(
            color: Tokens.surface,
            border: Border.all(
              color: highlighted
                  ? activeColor.withOpacity(0.55)
                  : Tokens.hairline,
              width: 1.0,
            ),
            borderRadius: BorderRadius.circular(Tokens.radiusPanel),
            boxShadow: highlighted
                ? [
                    BoxShadow(
                      color: activeColor.withOpacity(0.14),
                      blurRadius: 16,
                      offset: const Offset(0, 3),
                    ),
                  ]
                : null,
          ),
          child: child,
        ),
      ),
    );
  }
}

/// Unified PRTS panel accents painter: draws the top-right corner indicator
/// and/or the left-side active accent bar cleanly without non-uniform Border assertions.
class _AkPanelAccentPainter extends CustomPainter {
  const _AkPanelAccentPainter({
    required this.color,
    required this.drawCorner,
    required this.drawBar,
    this.cornerSize = 10.0,
    this.barWidth = 4.0,
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

class _SectionHeader extends StatelessWidget {
  const _SectionHeader({required this.title, this.subtitle, this.trailing});

  final String title;
  final String? subtitle;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final narrow = MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint;
    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: narrow && trailing != null
          ? Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              _buildTitle(),
              const SizedBox(height: 8),
              trailing!,
            ])
          : Row(children: [
              _buildTitle(),
              const Spacer(),
              if (trailing != null) trailing!,
            ]),
    );
  }

  Widget _buildTitle() => Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Container(
            width: Tokens.barWidth,
            height: 14,
            decoration: BoxDecoration(
              color: Tokens.cyan,
              borderRadius: BorderRadius.circular(1.0),
            ),
          ),
          const SizedBox(width: 8),
          Text(title, style: Tokens.section),
          if (subtitle != null) ...[
            const SizedBox(width: 8),
            Text(
              '// $subtitle',
              style: Tokens.akBilingualEn,
            ),
          ],
        ],
      );
}

/// AKDS .ak-hover-row: Tactical list tile with floating glowing rhombus (◇),
/// large semi-transparent uppercase English watermark sliding in on hover,
/// left cyan accent bar, and horizontal divider.
class _AkHoverRow extends StatefulWidget {
  const _AkHoverRow({
    required this.child,
    this.onTap,
    this.selected = false,
    this.watermark,
    this.previewTag,
    this.accentColor = Tokens.cyan,
    this.showDivider = true,
    this.padding = const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
  });

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
      duration: const Duration(milliseconds: 200),
      curve: Curves.easeOutCubic,
      clipBehavior: Clip.antiAlias,
      decoration: BoxDecoration(
        color: widget.selected
            ? accent.withOpacity(0.10)
            : (_hovered ? accent.withOpacity(0.05) : Colors.transparent),
        border: Border(
          left: BorderSide(
            color: widget.selected
                ? accent
                : (_hovered ? accent.withOpacity(0.85) : Colors.transparent),
            width: Tokens.barWidth,
          ),
          bottom: widget.showDivider
              ? BorderSide(
                  color: active
                      ? accent.withOpacity(0.3)
                      : Tokens.hairline.withOpacity(0.35),
                  width: 0.8,
                )
              : BorderSide.none,
        ),
      ),
      child: Stack(
        children: [
          // 1. Arknights Giant stylized English watermark sliding in
          if (widget.watermark != null && widget.watermark!.isNotEmpty)
            Positioned(
              right: -6,
              bottom: -6,
              child: IgnorePointer(
                child: AnimatedOpacity(
                  duration: const Duration(milliseconds: 240),
                  opacity: active ? (widget.selected ? 0.18 : 0.12) : 0.0,
                  child: AnimatedSlide(
                    duration: const Duration(milliseconds: 240),
                    curve: Curves.easeOutCubic,
                    offset: active ? Offset.zero : const Offset(0.12, 0),
                    child: Text(
                      widget.watermark!,
                      style: TextStyle(
                        fontFamily: 'Bender',
                        fontFamilyFallback: Tokens.fontFamilyFallback,
                        fontSize: 26,
                        fontWeight: FontWeight.w900,
                        letterSpacing: 2.2,
                        color: accent,
                      ),
                    ),
                  ),
                ),
              ),
            ),

          // 2. Tactical preview tag at top-right (e.g. "[ PRTS // NODE-01 ]")
          if (widget.previewTag != null && widget.previewTag!.isNotEmpty)
            Positioned(
              right: 8,
              top: 4,
              child: IgnorePointer(
                child: AnimatedOpacity(
                  duration: const Duration(milliseconds: 180),
                  opacity: active ? 0.85 : 0.0,
                  child: Text(
                    '[ ${widget.previewTag!} ]',
                    style: TextStyle(
                      fontFamily: 'JetBrains Mono',
                      fontFamilyFallback: Tokens.fontFamilyFallback,
                      fontSize: 8.5,
                      fontWeight: FontWeight.w700,
                      letterSpacing: 1.0,
                      color: accent,
                    ),
                  ),
                ),
              ),
            ),

          // 3. Foreground Row with floating rhombus (◇) indicator
          Padding(
            padding: widget.padding,
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                // Floating glowing Rhodes Island Diamond indicator (◇)
                AnimatedOpacity(
                  duration: const Duration(milliseconds: 180),
                  opacity: active ? 1.0 : 0.0,
                  child: AnimatedSlide(
                    duration: const Duration(milliseconds: 180),
                    curve: Curves.easeOutCubic,
                    offset: active ? Offset.zero : const Offset(-0.4, 0),
                    child: Container(
                      margin: const EdgeInsets.only(right: 8),
                      child: Transform.rotate(
                        angle: 0.785398, // 45 degrees
                        child: Container(
                          width: 6.5,
                          height: 6.5,
                          decoration: BoxDecoration(
                            color: accent,
                            boxShadow: [
                              BoxShadow(
                                color: accent.withOpacity(0.85),
                                blurRadius: 6,
                              ),
                            ],
                          ),
                        ),
                      ),
                    ),
                  ),
                ),
                // Main content
                Expanded(
                  child: AnimatedSlide(
                    duration: const Duration(milliseconds: 180),
                    curve: Curves.easeOutCubic,
                    offset: active ? const Offset(0.012, 0) : Offset.zero,
                    child: widget.child,
                  ),
                ),
              ],
            ),
          ),
        ],
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

/// AKDS .ak-hover-icon: Interactive icon with tactical preview feedback,
/// floating diamond indicator, holographic border glow, and preview HUD chip.
class _AkHoverIconButton extends StatefulWidget {
  const _AkHoverIconButton({
    required this.icon,
    required this.onPressed,
    this.tooltip,
    this.previewCode,
    this.color,
    this.size = 18.0,
    this.padding = const EdgeInsets.all(7),
  });

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
    final accent = widget.color ?? Tokens.cyan;

    Widget button = MouseRegion(
      onEnter: enabled ? (_) => setState(() => _hovered = true) : null,
      onExit: enabled ? (_) => setState(() => _hovered = false) : null,
      cursor: enabled ? SystemMouseCursors.click : SystemMouseCursors.basic,
      child: GestureDetector(
        onTap: widget.onPressed,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 180),
          padding: widget.padding,
          decoration: BoxDecoration(
            color: _hovered && enabled
                ? accent.withOpacity(0.12)
                : Colors.transparent,
            border: Border.all(
              color: _hovered && enabled
                  ? accent.withOpacity(0.7)
                  : Colors.transparent,
              width: 1.0,
            ),
            boxShadow: _hovered && enabled
                ? [
                    BoxShadow(
                      color: accent.withOpacity(0.25),
                      blurRadius: 8,
                      offset: const Offset(0, 1),
                    ),
                  ]
                : null,
          ),
          child: Stack(
            clipBehavior: Clip.none,
            alignment: Alignment.center,
            children: [
              // Floating glowing diamond at top-right
              if (_hovered && enabled)
                Positioned(
                  top: -3,
                  right: -3,
                  child: Transform.rotate(
                    angle: 0.785398,
                    child: Container(
                      width: 5,
                      height: 5,
                      decoration: BoxDecoration(
                        color: accent,
                        boxShadow: [
                          BoxShadow(
                            color: accent.withOpacity(0.9),
                            blurRadius: 4,
                          ),
                        ],
                      ),
                    ),
                  ),
                ),
              // Icon with scale feedback
              AnimatedScale(
                scale: _hovered && enabled ? 1.14 : 1.0,
                duration: const Duration(milliseconds: 180),
                curve: Curves.easeOutCubic,
                child: widget.icon,
              ),
            ],
          ),
        ),
      ),
    );

    if (widget.tooltip != null) {
      return Tooltip(
        message: widget.previewCode != null
            ? '// ${widget.previewCode!} · ${widget.tooltip!}'
            : widget.tooltip!,
        textStyle: const TextStyle(
          fontFamily: 'JetBrains Mono',
          fontSize: 11,
          color: Tokens.ink,
        ),
        decoration: BoxDecoration(
          color: Tokens.surfaceElevated,
          border: Border.all(color: accent.withOpacity(0.6), width: 1),
          boxShadow: [
            BoxShadow(
              color: accent.withOpacity(0.2),
              blurRadius: 8,
              offset: const Offset(0, 2),
            ),
          ],
        ),
        child: button,
      );
    }

    return button;
  }
}

/// Wraps any action button with Arknights tactical hover aura and glowing rhombus.
class _AkHoverButtonWrap extends StatefulWidget {
  const _AkHoverButtonWrap({
    required this.child,
    this.color = Tokens.cyan,
  });

  final Widget child;
  final Color color;

  @override
  State<_AkHoverButtonWrap> createState() => _AkHoverButtonWrapState();
}

class _AkHoverButtonWrapState extends State<_AkHoverButtonWrap> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 200),
        curve: Curves.easeOutCubic,
        transform: _hovered
            ? Matrix4.translationValues(0.0, -1.0, 0.0)
            : Matrix4.identity(),
        decoration: BoxDecoration(
          boxShadow: _hovered
              ? [
                  BoxShadow(
                    color: widget.color.withOpacity(0.25),
                    blurRadius: 10,
                    offset: const Offset(0, 2),
                  ),
                ]
              : null,
        ),
        child: Stack(
          clipBehavior: Clip.none,
          children: [
            widget.child,
            if (_hovered)
              Positioned(
                top: -2,
                left: -2,
                child: Transform.rotate(
                  angle: 0.785398,
                  child: Container(
                    width: 5,
                    height: 5,
                    decoration: BoxDecoration(
                      color: widget.color,
                      boxShadow: [
                        BoxShadow(
                          color: widget.color.withOpacity(0.9),
                          blurRadius: 5,
                        ),
                      ],
                    ),
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }
}

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
        decoration: BoxDecoration(
          color: tint ?? Tokens.idleTint,
          borderRadius: BorderRadius.circular(Tokens.radiusBadge),
          border: Border.all(color: color.withOpacity(0.35), width: 0.9),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (showDot) ...[
              Container(
                width: 5,
                height: 5,
                decoration: BoxDecoration(
                  color: color,
                  borderRadius: BorderRadius.circular(1.0),
                ),
              ),
              const SizedBox(width: 5),
            ],
            Text(label,
                style: TextStyle(
                    fontSize: 11,
                    fontWeight: FontWeight.w700,
                    color: color,
                    letterSpacing: 0.3)),
          ],
        ),
      );
}

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
            BoxShadow(color: color.withOpacity(0.55), blurRadius: 6),
          ],
        ),
      );
}

enum _NoticeKind { ok, warn, bad, info }

class _Notice extends StatelessWidget {
  const _Notice({required this.text, required this.kind, this.action});

  final String text;
  final _NoticeKind kind;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final (color, tint, icon) = switch (kind) {
      _NoticeKind.ok => (Tokens.ok, Tokens.okTint, Icons.check_circle_rounded),
      _NoticeKind.warn =>
        (Tokens.warn, Tokens.warnTint, Icons.warning_amber_rounded),
      _NoticeKind.bad => (Tokens.bad, Tokens.badTint, Icons.error_outline_rounded),
      _NoticeKind.info =>
        (Tokens.cyan, Tokens.cyanTint, Icons.info_outline_rounded),
    };
    final isAlert = kind == _NoticeKind.warn || kind == _NoticeKind.bad;
    return ClipRRect(
      borderRadius: BorderRadius.circular(Tokens.radiusControl),
      child: Container(
        decoration: BoxDecoration(
          color: tint,
          border: Border(
            top: BorderSide(color: color.withOpacity(0.4), width: 1),
            right: BorderSide(color: color.withOpacity(0.4), width: 1),
            bottom: BorderSide(color: color.withOpacity(0.4), width: 1),
            left: BorderSide(color: color, width: Tokens.barWidth),
          ),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (isAlert)
              _AkStripes(
                height: 3,
                color: color.withOpacity(0.55),
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
                          Text(text,
                              style: TextStyle(
                                  color: color,
                                  fontSize: 12.5,
                                  height: 1.45,
                                  fontWeight: FontWeight.w500)),
                          if (action != null)
                            Padding(
                                padding: const EdgeInsets.only(top: 4),
                                child: Align(
                                    alignment: Alignment.centerLeft,
                                    child: action!)),
                        ]),
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

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.text, this.actionLabel, this.onAction});

  final String text;
  final String? actionLabel;
  final VoidCallback? onAction;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 26),
        child: Column(children: [
          const Icon(Icons.inbox_outlined, size: 26, color: Tokens.inkFaint),
          const SizedBox(height: 10),
          Text(text, textAlign: TextAlign.center, style: Tokens.small),
          if (actionLabel != null) ...[
            const SizedBox(height: 12),
            _JellyTap(
              onTap: onAction,
              child: TextButton(onPressed: onAction, child: Text(actionLabel!)),
            ),
          ],
        ]),
      );
}

/// A readout line: label in muted type, value in tabular figures.
class _Stats extends StatelessWidget {
  const _Stats({required this.items});

  final List<(String, String)> items;

  @override
  Widget build(BuildContext context) => Wrap(
        spacing: 26,
        runSpacing: 12,
        children: [
          for (final (label, value) in items)
            Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(label, style: Tokens.hint),
              const SizedBox(height: 2),
              Text(value, style: Tokens.number),
            ]),
        ],
      );
}

class _KeyValueRow extends StatefulWidget {
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
  State<_KeyValueRow> createState() => _KeyValueRowState();
}

class _KeyValueRowState extends State<_KeyValueRow> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 180),
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
        decoration: BoxDecoration(
          color: _hovered ? Tokens.cyan.withOpacity(0.05) : Colors.transparent,
          border: Border(
            left: BorderSide(
              color: _hovered ? Tokens.cyan.withOpacity(0.7) : Colors.transparent,
              width: 2.5,
            ),
            bottom: BorderSide(
              color: _hovered ? Tokens.cyan.withOpacity(0.15) : Tokens.hairline.withOpacity(0.15),
              width: 0.5,
            ),
          ),
        ),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          AnimatedOpacity(
            duration: const Duration(milliseconds: 150),
            opacity: _hovered ? 1.0 : 0.0,
            child: Container(
              margin: const EdgeInsets.only(top: 4, right: 6),
              child: Transform.rotate(
                angle: 0.785398,
                child: Container(
                  width: 4.5,
                  height: 4.5,
                  decoration: BoxDecoration(
                    color: Tokens.cyan,
                    boxShadow: [
                      BoxShadow(
                        color: Tokens.cyan.withOpacity(0.8),
                        blurRadius: 4,
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
          SizedBox(
              width: MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint
                  ? 96
                  : 126,
              child: Text(widget.label, style: Tokens.hint)),
          Expanded(
            child: Text(widget.value,
                style: Tokens.number.copyWith(
                    fontWeight: FontWeight.w400,
                    color: widget.valueColor ?? (_hovered ? Tokens.cyanNeon : Tokens.ink))),
          ),
          if (widget.trailing != null) widget.trailing!,
        ]),
      ),
    );
  }
}

double dialogWidth(BuildContext context, double designed) =>
    MediaQuery.sizeOf(context).width < Tokens.mobileBreakpoint
        ? double.maxFinite
        : designed;

/// The live route: where traffic enters, which node carries it, where it exits.
class _PathSurface extends StatelessWidget {
  const _PathSurface({
    required this.stateLabel,
    required this.nodeLabel,
    required this.regionLabel,
    required this.connected,
    required this.stats,
  });

  final String stateLabel;
  final String nodeLabel;
  final String regionLabel;
  final bool connected;
  final List<(String, String)> stats;

  @override
  Widget build(BuildContext context) => _Panel(
        highlighted: connected,
        showCorner: true,
        padding: const EdgeInsets.fromLTRB(20, 18, 20, 18),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            _StatusDot(color: connected ? Tokens.ok : Tokens.inkFaint),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                stateLabel,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(
                    fontSize: 13, fontWeight: FontWeight.w700, letterSpacing: 0.3),
              ),
            ),
            const SizedBox(width: 8),
            Text('// ROUTING_MATRIX · PRTS',
                style: Tokens.akBilingualEn.copyWith(fontSize: 10)),
          ]),
          const SizedBox(height: 16),
          Row(children: [
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 3),
              decoration: BoxDecoration(
                color: Tokens.surfaceElevated,
                borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                border: Border.all(color: Tokens.hairlineBright),
              ),
              child: const Text('本机', style: TextStyle(fontSize: 12, fontWeight: FontWeight.w600)),
            ),
            const SizedBox(width: 10),
            Expanded(child: _Connector(active: connected)),
            const SizedBox(width: 10),
            Flexible(
              child: Container(
                padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 4),
                decoration: BoxDecoration(
                  color: connected ? Tokens.cyan.withOpacity(0.12) : Tokens.surfaceElevated,
                  borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                  border: Border.all(
                    color: connected ? Tokens.cyan.withOpacity(0.4) : Tokens.hairline,
                  ),
                ),
                child: Text(
                  nodeLabel,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontSize: 12.5,
                    fontWeight: FontWeight.w700,
                    color: connected ? Tokens.cyan : Tokens.ink,
                  ),
                ),
              ),
            ),
            const SizedBox(width: 8),
            const Icon(Icons.arrow_right_alt_rounded,
                size: 18, color: Tokens.inkFaint),
            const SizedBox(width: 4),
            _Badge(
                label: regionLabel,
                color: connected ? Tokens.cyan : Tokens.inkMuted,
                tint: connected ? Tokens.cyanTint : Tokens.idleTint,
                showDot: true),
          ]),
          const SizedBox(height: 18),
          const Divider(),
          const SizedBox(height: 14),
          _Stats(items: stats),
        ]),
      );
}

class _Connector extends StatelessWidget {
  const _Connector({required this.active});

  final bool active;

  @override
  Widget build(BuildContext context) => Row(children: [
        Expanded(
            child: Container(
                height: 1.5,
                color: active ? Tokens.cyan.withOpacity(0.5) : Tokens.hairline)),
        Container(
          width: 7,
          height: 7,
          decoration: BoxDecoration(
              color: active ? Tokens.cyan : Tokens.inkFaint,
              shape: BoxShape.circle,
              boxShadow: active
                  ? [BoxShadow(color: Tokens.cyan.withOpacity(0.6), blurRadius: 6)]
                  : null),
        ),
        Expanded(
            child: Container(
                height: 1.5,
                color: active ? Tokens.cyan.withOpacity(0.5) : Tokens.hairline)),
      ]);
}

/// A titled group of label lines, used by the diagnostic dialogs.
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

/// Hero Industrial Power Switch Control Component for the Connect Page
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
  Widget build(BuildContext context) {
    final activeColor =
        connected ? Tokens.cyan : (busy ? Tokens.warn : Tokens.blueBright);
    final isClickable = !busy && port != null;
    final isMeasureable = !busy && connected;

    return _AkBrackets(
      child: _Panel(
        highlighted: connected,
        showCorner: true,
        padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2.5),
                  decoration: BoxDecoration(
                    color: activeColor.withOpacity(0.18),
                    borderRadius: BorderRadius.circular(Tokens.radiusBadge),
                    border: Border.all(color: activeColor.withOpacity(0.4)),
                  ),
                  child: Text(
                    connected
                        ? 'SYS // ONLINE'
                        : (busy ? 'SYS // BUSY' : 'SYS // STANDBY'),
                    style: TextStyle(
                      fontSize: 10.5,
                      fontWeight: FontWeight.w700,
                      color: activeColor,
                      letterSpacing: 0.8,
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    'CORE_SWITCH // 01 · PRTS-PWR',
                    overflow: TextOverflow.ellipsis,
                    style: Tokens.akBilingualEn.copyWith(color: Tokens.inkMuted),
                  ),
                ),
                const SizedBox(width: 8),
                _StatusDot(
                    color: connected
                        ? Tokens.ok
                        : (busy ? Tokens.warn : Tokens.inkFaint),
                    size: 9),
              ],
            ),
            const SizedBox(height: 14),
            if (!connected && !busy)
              Padding(
                padding: const EdgeInsets.only(bottom: 14),
                child: _AkStripes(
                  height: 3,
                  color: Tokens.hairlineBright,
                  background: Colors.transparent,
                ),
              ),
            LayoutBuilder(
              builder: (context, constraints) {
                final isNarrow = constraints.maxWidth < 450;
                final powerSwitch = _TacticalPowerDial(
                  connected: connected,
                  busy: busy,
                  enabled: isClickable,
                  onTap: isClickable ? onToggle : null,
                );
                final actionControls = Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Wrap(
                      spacing: 10,
                      runSpacing: 10,
                      crossAxisAlignment: WrapCrossAlignment.center,
                      children: [
                        _JellyTap(
                          enabled: isClickable,
                          onTap: isClickable ? onToggle : null,
                          child: FilledButton.icon(
                            onPressed: isClickable ? onToggle : null,
                            icon: const Icon(Icons.power_settings_new_rounded,
                                size: 18),
                            label:
                                Text(busy ? '处理中…' : (connected ? '断开连接' : '连接')),
                            style: ButtonStyle(
                              backgroundColor:
                                  WidgetStateProperty.resolveWith((states) {
                                if (states.contains(WidgetState.disabled)) {
                                  return Tokens.idleTint;
                                }
                                return connected ? Tokens.bad : Tokens.blue;
                              }),
                              elevation:
                                  WidgetStatePropertyAll(connected ? 6 : 2),
                              shadowColor: WidgetStatePropertyAll(
                                connected
                                    ? Tokens.bad.withOpacity(0.4)
                                    : Tokens.blue.withOpacity(0.4),
                              ),
                            ),
                          ),
                        ),
                        _JellyTap(
                          enabled: isMeasureable,
                          onTap: isMeasureable ? onMeasure : null,
                          child: OutlinedButton.icon(
                            onPressed: isMeasureable ? onMeasure : null,
                            icon: const Icon(Icons.speed_rounded, size: 18),
                            label: Text(latency == null ? '测速' : '$latency ms'),
                          ),
                        ),
                      ],
                    ),
                    const SizedBox(height: 12),
                    Text(
                      statusDetail,
                      style: Tokens.small.copyWith(height: 1.45),
                    ),
                  ],
                );

                if (isNarrow) {
                  return Column(
                    children: [
                      Center(child: powerSwitch),
                      const SizedBox(height: 16),
                      actionControls,
                    ],
                  );
                }
                return Row(
                  children: [
                    powerSwitch,
                    const SizedBox(width: 22),
                    Expanded(child: actionControls),
                  ],
                );
              },
            ),
          ],
        ),
      ),
    );
  }
}

class _TacticalPowerDial extends StatelessWidget {
  const _TacticalPowerDial({
    required this.connected,
    required this.busy,
    required this.enabled,
    required this.onTap,
  });

  final bool connected;
  final bool busy;
  final bool enabled;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final glowColor =
        connected ? Tokens.cyan : (busy ? Tokens.warn : Tokens.blueBright);

    return SizedBox(
      width: 110,
      height: 110,
      child: Stack(
        alignment: Alignment.center,
        children: [
          // Outer Concentric Wave Ripples (when connected or busy)
          _RippleWave(
            active: connected || busy,
            color: glowColor,
            size: 110,
          ),
          // Tactical Bezel Crosshair Ticks Painter
          CustomPaint(
            size: const Size(96, 96),
            painter: _DialBezelPainter(
              color: (connected || busy)
                  ? glowColor.withOpacity(0.7)
                  : Tokens.hairlineBright,
            ),
          ),
          // Tactical Dial Background and Border
          Container(
            width: 78,
            height: 78,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: Tokens.surfaceElevated,
              border: Border.all(
                color: (connected || busy) ? glowColor : Tokens.hairlineBright,
                width: 2.2,
              ),
              boxShadow: (connected || busy)
                  ? [
                      BoxShadow(
                        color: glowColor.withOpacity(0.35),
                        blurRadius: 16,
                        spreadRadius: 2,
                      ),
                    ]
                  : null,
            ),
          ),
          // Central Tactical Power Switch with Jelly Bounce
          _JellyTap(
            enabled: enabled,
            onTap: onTap,
            child: Material(
              color: Colors.transparent,
              child: InkWell(
                onTap: enabled ? onTap : null,
                customBorder: const CircleBorder(),
                splashColor: glowColor.withOpacity(0.3),
                highlightColor: glowColor.withOpacity(0.15),
                child: Container(
                  width: 72,
                  height: 72,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    gradient: RadialGradient(
                      colors: [
                        glowColor.withOpacity(connected ? 0.28 : 0.12),
                        Colors.transparent,
                      ],
                    ),
                  ),
                  child: Icon(
                    Icons.power_settings_new_rounded,
                    size: 34,
                    color: enabled
                        ? (connected
                            ? Tokens.cyanNeon
                            : (busy ? Tokens.warn : Tokens.blueBright))
                        : Tokens.inkFaint,
                  ),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// Painter for the 4 tactical tick marks around the power dial bezel
class _DialBezelPainter extends CustomPainter {
  const _DialBezelPainter({required this.color});
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final center = Offset(size.width / 2, size.height / 2);
    final radius = size.width / 2;
    final paint = Paint()
      ..color = color
      ..strokeWidth = 1.5
      ..style = PaintingStyle.stroke;

    // 4 cardinal tick marks
    const tickLen = 5.0;
    canvas.drawLine(Offset(center.dx, center.dy - radius),
        Offset(center.dx, center.dy - radius + tickLen), paint);
    canvas.drawLine(Offset(center.dx, center.dy + radius),
        Offset(center.dx, center.dy + radius - tickLen), paint);
    canvas.drawLine(Offset(center.dx - radius, center.dy),
        Offset(center.dx - radius + tickLen, center.dy), paint);
    canvas.drawLine(Offset(center.dx + radius, center.dy),
        Offset(center.dx + radius - tickLen, center.dy), paint);
  }

  @override
  bool shouldRepaint(covariant _DialBezelPainter oldDelegate) =>
      oldDelegate.color != color;
}
