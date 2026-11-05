import 'package:flutter/material.dart';

/// Industrial Sci-Fi & Arknights Rhodes Island Operations Terminal Design Tokens (AKDS).
/// Features a near-black charcoal canvas, ivory typography, industrial yellow primary accents,
/// restrained telemetry cyan, crisp typographic hierarchy, and sharp cut-corner geometry.
abstract final class Tokens {
  // AKDS Official Primitives
  static const akGray0 = Color(0xFFFFFFFF);
  static const akGray50 = Color(0xFFF5F5F5);
  static const akGray800 = Color(0xFF313131);
  static const akGray850 = Color(0xFF272727);
  static const akGray900 = Color(0xFF1D1F20);
  static const akGray950 = Color(0xFF14161A);
  static const akGray1000 = Color(0xFF000000);

  // Rhodes Island Industrial Yellow (Primary Action & Industrial Accent)
  static const yellow = Color(0xFFFFD800);
  static const yellowHover = Color(0xFFFFE23A);
  static const yellowDark = Color(0xFFD6B200);
  static const yellowTint = Color(0x24FFD800);
  static const yellowGlow = Color(0x38FFD800);
  static const akYellow500 = yellow;

  // Restrained Telemetry Cyan & Tactical Blue (Exclusively for routes & telemetry)
  static const cyan = Color(0xFF18D1FF);
  static const cyanNeon = Color(0xFF42E4FF);
  static const cyanGlow = Color(0x3818D1FF);
  static const cyanTint = Color(0x1F18D1FF);
  static const akCyan500 = cyan;
  static const akCyan400 = Color(0xFF22BBFF);
  static const akCyan200 = Color(0xFF7EE5FF);

  static const blue = Color(0xFF0098DC);
  static const blueBright = Color(0xFF00AAFC);
  static const blueGlow = Color(0x380098DC);
  static const akBlue500 = blue;
  static const akBlue400 = Color(0xFF00B0FF);

  // Backward-compatible aliases for existing references
  static const jade = cyan;
  static const jadeDeep = blueBright;
  static const jadeTint = cyanTint;

  // Signal & Status Colours
  static const ok = Color(0xFF4FFAA5);
  static const okTint = Color(0x224FFAA5);
  static const warn = yellow;
  static const warnTint = yellowTint;
  static const bad = Color(0xFFC82A36);
  static const badTint = Color(0x28C82A36);
  static const idle = Color(0xFF6E8094);
  static const idleTint = Color(0x206E8094);
  static const recovery = akBlue400;

  // AKDS Tactical Dark Theme Palette (Operations Terminal)
  static const background =
      Color(0xFF121418); // Deep charcoal operations canvas
  static const rail = Color(0xFF171A21); // Tactical sidebar / bottom nav
  static const surface = Color(0xFF1C212A); // Operations panel surface
  static const surfaceElevated = Color(0xFF242B36); // Elevated tactical card
  static const hairline = Color(0xFF303846); // Precision technical line
  static const hairlineBright = Color(0xFF465264); // Highlight technical line

  // Ivory & Muted Typography
  static const ink = Color(0xFFF6F7FA); // High-contrast ivory text
  static const inkMuted = Color(0xFF9CA9B8); // Secondary technical labels
  static const inkFaint = Color(0xFF5D6B7D); // Subdued technical markings

  // Geometry - AKDS strict rectangular sharp look with subtle 45° chamfers
  static const radiusPanel = 3.0;
  static const mobileBreakpoint = 700.0;
  static const radiusControl = 2.0;
  static const radiusBadge = 2.0;
  static const barWidth = 3.5; // Standard tactical 3.5px accent bar
  static const akCutSm = 3.0; // 45° chamfer cut small
  static const akCutMd = 6.0; // 45° chamfer cut medium
  static const akCutLg = 10.0; // 45° chamfer cut large

  static const railWidth = 210.0;
  static const contentMaxWidth = 920.0;

  static const fontFamily = 'Microsoft YaHei UI';
  static const fontFamilyFallback = <String>[
    'Bender',
    'JetBrains Mono',
    'Chakra Petch',
    'Segoe UI',
    'Roboto',
    'sans-serif',
  ];
  static const fontMonoFallback = <String>[
    'JetBrains Mono',
    'Consolas',
    'Liberation Mono',
    'monospace',
  ];

  static const pageTitle = TextStyle(
      fontSize: 18,
      fontWeight: FontWeight.w800,
      color: ink,
      height: 1.25,
      letterSpacing: 0.6);
  static const section = TextStyle(
      fontSize: 13.5,
      fontWeight: FontWeight.w700,
      color: ink,
      height: 1.3,
      letterSpacing: 0.4);
  static const body = TextStyle(
      fontFamily: fontFamily,
      fontFamilyFallback: fontFamilyFallback,
      fontSize: 13,
      color: ink,
      height: 1.55);
  static const small = TextStyle(
      fontFamily: fontFamily,
      fontFamilyFallback: fontFamilyFallback,
      fontSize: 12,
      color: inkMuted,
      height: 1.5);
  static const hint = TextStyle(
      fontSize: 11.5, color: inkFaint, height: 1.45, letterSpacing: 0.2);

  // Technical PRTS Typography Tokens
  static const akDisplay = TextStyle(
      fontSize: 22,
      fontWeight: FontWeight.w900,
      color: ink,
      letterSpacing: 0.8,
      fontFamilyFallback: fontFamilyFallback);
  static const akOverline = TextStyle(
      fontSize: 9.5,
      fontWeight: FontWeight.w700,
      color: inkMuted,
      letterSpacing: 1.5,
      fontFamilyFallback: fontFamilyFallback);
  static const akBilingualEn = TextStyle(
      fontSize: 9.0,
      fontWeight: FontWeight.w700,
      color: yellow,
      letterSpacing: 1.1,
      fontFamilyFallback: fontFamilyFallback);
  static const akCodeId = TextStyle(
      fontSize: 11,
      fontWeight: FontWeight.w700,
      color: cyan,
      letterSpacing: 0.8,
      fontFamily: 'Consolas',
      fontFamilyFallback: fontMonoFallback);

  /// Tabular figures keep columns of changing numbers from shifting.
  static const _tabular = [FontFeature.tabularFigures()];
  static const number = TextStyle(
      fontSize: 12.5,
      fontWeight: FontWeight.w600,
      color: ink,
      letterSpacing: 0.4,
      fontFamily: 'Consolas',
      fontFamilyFallback: fontMonoFallback,
      fontFeatures: _tabular);
  static const numberStrong = TextStyle(
      fontSize: 15,
      fontWeight: FontWeight.w700,
      color: ink,
      letterSpacing: 0.5,
      fontFamily: 'Consolas',
      fontFamilyFallback: fontMonoFallback,
      fontFeatures: _tabular);

  static ThemeData theme() {
    final base = ThemeData.dark(useMaterial3: true);
    const shape = BeveledRectangleBorder(
        borderRadius: BorderRadius.all(Radius.circular(akCutSm)));

    return base.copyWith(
      scaffoldBackgroundColor: background,
      colorScheme: const ColorScheme.dark(
        primary: yellow,
        onPrimary: Color(0xFF111317),
        secondary: cyan,
        onSecondary: Color(0xFF111317),
        surface: surface,
        onSurface: ink,
        error: bad,
        onError: Colors.white,
      ),
      textTheme: base.textTheme.apply(
        fontFamily: fontFamily,
        fontFamilyFallback: fontFamilyFallback,
        bodyColor: ink,
        displayColor: ink,
      ),
      dividerTheme:
          const DividerThemeData(color: hairline, thickness: 1, space: 1),
      iconTheme: const IconThemeData(color: inkMuted, size: 18),
      filledButtonTheme: FilledButtonThemeData(
        style: ButtonStyle(
          backgroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled) ? idleTint : yellow),
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled)
                  ? inkFaint
                  : const Color(0xFF111317)),
          elevation: const WidgetStatePropertyAll(0),
          padding: const WidgetStatePropertyAll(
              EdgeInsets.symmetric(horizontal: 18, vertical: 13)),
          minimumSize: const WidgetStatePropertyAll(Size(48, 48)),
          shape: const WidgetStatePropertyAll(shape),
          side: WidgetStateProperty.resolveWith((states) => BorderSide(
              color: states.contains(WidgetState.disabled)
                  ? Colors.transparent
                  : yellowDark,
              width: 0.9)),
          textStyle: const WidgetStatePropertyAll(TextStyle(
              fontFamily: fontFamily,
              fontFamilyFallback: fontFamilyFallback,
              fontSize: 13,
              fontWeight: FontWeight.w800,
              letterSpacing: 0.6)),
        ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: ButtonStyle(
          backgroundColor: const WidgetStatePropertyAll(surface),
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled) ? inkFaint : ink),
          side: WidgetStateProperty.resolveWith((states) => BorderSide(
              color: states.contains(WidgetState.hovered)
                  ? yellow
                  : hairlineBright,
              width: 0.9)),
          padding: const WidgetStatePropertyAll(
              EdgeInsets.symmetric(horizontal: 16, vertical: 12)),
          minimumSize: const WidgetStatePropertyAll(Size(48, 48)),
          shape: const WidgetStatePropertyAll(shape),
          textStyle: const WidgetStatePropertyAll(TextStyle(
              fontFamily: fontFamily,
              fontFamilyFallback: fontFamilyFallback,
              fontSize: 13,
              fontWeight: FontWeight.w600)),
        ),
      ),
      textButtonTheme: TextButtonThemeData(
        style: ButtonStyle(
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled) ? inkFaint : yellow),
          textStyle: const WidgetStatePropertyAll(TextStyle(
              fontFamily: fontFamily,
              fontFamilyFallback: fontFamilyFallback,
              fontSize: 13,
              fontWeight: FontWeight.w700)),
          padding: const WidgetStatePropertyAll(
              EdgeInsets.symmetric(horizontal: 12, vertical: 10)),
          minimumSize: const WidgetStatePropertyAll(Size(48, 48)),
          shape: const WidgetStatePropertyAll(shape),
        ),
      ),
      segmentedButtonTheme: SegmentedButtonThemeData(
        style: ButtonStyle(
          backgroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.selected) ? yellowTint : surface),
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.selected) ? yellow : inkMuted),
          side: WidgetStateProperty.resolveWith((states) => BorderSide(
              color: states.contains(WidgetState.selected)
                  ? yellow.withValues(alpha: 0.65)
                  : hairline)),
          textStyle: const WidgetStatePropertyAll(TextStyle(
              fontFamily: fontFamily,
              fontFamilyFallback: fontFamilyFallback,
              fontSize: 12.5,
              fontWeight: FontWeight.w700)),
          shape: const WidgetStatePropertyAll(shape),
        ),
      ),
      inputDecorationTheme: InputDecorationTheme(
        isDense: true,
        filled: true,
        fillColor: surface,
        contentPadding:
            const EdgeInsets.symmetric(horizontal: 12, vertical: 13),
        border: OutlineInputBorder(
            borderRadius: BorderRadius.circular(radiusControl),
            borderSide: const BorderSide(color: hairline)),
        enabledBorder: OutlineInputBorder(
            borderRadius: BorderRadius.circular(radiusControl),
            borderSide: const BorderSide(color: hairline)),
        focusedBorder: OutlineInputBorder(
            borderRadius: BorderRadius.circular(radiusControl),
            borderSide: const BorderSide(color: yellow, width: 1.5)),
        labelStyle: small,
        floatingLabelStyle: const TextStyle(
            fontSize: 12, color: yellow, fontWeight: FontWeight.w700),
        hintStyle: hint,
        prefixIconColor: inkFaint,
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: surfaceElevated,
        surfaceTintColor: Colors.transparent,
        shape: BeveledRectangleBorder(
            borderRadius: BorderRadius.circular(akCutMd),
            side: const BorderSide(color: hairlineBright, width: 1.0)),
        titleTextStyle: section,
        contentTextStyle: body,
      ),
      snackBarTheme: SnackBarThemeData(
        backgroundColor: surfaceElevated,
        contentTextStyle: const TextStyle(
            fontFamily: fontFamily,
            fontFamilyFallback: fontFamilyFallback,
            fontSize: 13,
            color: ink),
        behavior: SnackBarBehavior.floating,
        shape: BeveledRectangleBorder(
            borderRadius: BorderRadius.circular(akCutSm),
            side: const BorderSide(color: yellow, width: 1)),
      ),
      progressIndicatorTheme:
          const ProgressIndicatorThemeData(color: yellow, linearMinHeight: 3),
    );
  }
}
