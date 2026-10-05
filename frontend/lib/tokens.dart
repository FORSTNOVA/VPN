import 'dart:ui';
import 'package:flutter/material.dart';

/// Industrial Sci-Fi & Arknights PRTS Design System Tokens (AKDS).
/// Features high-contrast dark tactical surfaces, electric blue & neon cyan accents,
/// crisp industrial typography, and sharp chamfered geometry.
abstract final class Tokens {
  // AKDS Official Primitives (from mooncellwiki PRTS Design)
  static const akGray0 = Color(0xFFFFFFFF);
  static const akGray50 = Color(0xFFF5F5F5);
  static const akGray800 = Color(0xFF313131);  // In-game standard button
  static const akGray850 = Color(0xFF272727);
  static const akGray900 = Color(0xFF1D1F20);  // Site panel
  static const akGray950 = Color(0xFF181818);  // In-game deep canvas
  static const akGray1000 = Color(0xFF000000);

  static const akCyan200 = Color(0xFF7EE5FF);
  static const akCyan400 = Color(0xFF22BBFF);
  static const akCyan500 = Color(0xFF18D1FF);  // ★ AKDS site brand accent #18d1ff
  static const akBlue400 = Color(0xFF00B0FF);
  static const akBlue500 = Color(0xFF0098DC);  // ★ In-game UI blue #0098dc
  static const akBlue600 = Color(0xFF0075A9);  // ★ In-game toggle-on #0075a9
  static const akYellow500 = Color(0xFFFFD800); // ★ In-game yellow #ffd800
  static const akRed600 = Color(0xFFC82A36);   // ★ In-game NEW mark #c82a36
  static const akRed700 = Color(0xFFA40000);   // ★ In-game BREAKING NEWS
  static const akRed800 = Color(0xFF711111);   // ★ In-game confirm button
  static const akGreen400 = Color(0xFF4FFAA5); // ★ Active status green
  static const akGreen500 = Color(0xFF2FAC78);
  static const akOrange500 = Color(0xFFF49800);

  // AKDS Tactical Dark Theme Palette (Night / Terminal Mode)
  static const background = Color(0xFF101216); // Deep Arknights canvas
  static const rail = Color(0xFF16191F);       // Tactical sidebar
  static const surface = Color(0xFF1D2128);    // Industrial panel surface
  static const surfaceElevated = Color(0xFF242A34);
  static const hairline = Color(0xFF333B47);   // Precision border line
  static const hairlineBright = Color(0xFF495465); // Highlight border

  // Text ink colors
  static const ink = Color(0xFFF2F6FA);        // High contrast primary text
  static const inkMuted = Color(0xFF90A1B5);   // Secondary technical labels
  static const inkFaint = Color(0xFF5A6B80);   // Subdued metadata & markings

  // Signature Arknights PRTS Neon Cyan & Tactical Blue
  static const cyan = akCyan500;               // Official PRTS Cyan #18D1FF
  static const cyanNeon = Color(0xFF42E4FF);
  static const cyanGlow = Color(0x3818D1FF);
  static const cyanTint = Color(0x1F18D1FF);

  static const blue = akBlue500;               // Tactical Action Blue #0098DC
  static const blueBright = Color(0xFF00AAFC); // High-contrast interactive blue
  static const blueGlow = Color(0x380098DC);

  // Backward-compatible aliases for existing references
  static const jade = cyan;
  static const jadeDeep = blueBright;
  static const jadeTint = cyanTint;

  // Signal & Status Colours
  static const ok = akGreen400;                // Terminal active green #4FFAA5
  static const okTint = Color(0x224FFAA5);
  static const warn = akYellow500;             // In-game warning amber #FFD800
  static const warnTint = Color(0x28FFD800);
  static const bad = akRed600;                 // Originium alert red #C82A36
  static const badTint = Color(0x28C82A36);
  static const idle = Color(0xFF6E8094);       // Standby grey
  static const idleTint = Color(0x206E8094);
  static const recovery = akBlue400;

  // Geometry - AKDS strict rectangular sharp look with subtle chamfer
  static const radiusPanel = 4.0;
  static const mobileBreakpoint = 700.0;
  static const radiusControl = 2.0;
  static const radiusBadge = 2.0;
  static const barWidth = 4.0;                 // AKDS standard 4px accent bar

  static const railWidth = 224.0;
  static const contentMaxWidth = 880.0;

  static const fontFamily = 'Microsoft YaHei UI';
  static const fontFamilyFallback = <String>[
    'Bender',
    'JetBrains Mono',
    'Oswald',
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
      fontSize: 19, fontWeight: FontWeight.w700, color: ink, height: 1.3, letterSpacing: 0.5);
  static const section = TextStyle(
      fontSize: 13.5, fontWeight: FontWeight.w700, color: ink, height: 1.3, letterSpacing: 0.4);
  static const body = TextStyle(fontSize: 13, color: ink, height: 1.55);
  static const small = TextStyle(fontSize: 12, color: inkMuted, height: 1.5);
  static const hint = TextStyle(fontSize: 11.5, color: inkFaint, height: 1.5, letterSpacing: 0.3);

  // AKDS Signature Typography Tokens
  static const akDisplay = TextStyle(
      fontSize: 22,
      fontWeight: FontWeight.w800,
      color: ink,
      letterSpacing: 0.8,
      fontFamilyFallback: fontFamilyFallback);
  static const akOverline = TextStyle(
      fontSize: 10,
      fontWeight: FontWeight.w700,
      color: inkMuted,
      letterSpacing: 1.4,
      fontFamilyFallback: fontFamilyFallback);
  static const akBilingualEn = TextStyle(
      fontSize: 9.5,
      fontWeight: FontWeight.w700,
      color: cyan,
      letterSpacing: 1.2,
      fontFamilyFallback: fontFamilyFallback);
  static const akCodeId = TextStyle(
      fontSize: 11,
      fontWeight: FontWeight.w700,
      color: cyan,
      letterSpacing: 1.0,
      fontFamily: 'Consolas',
      fontFamilyFallback: fontMonoFallback);

  /// Tabular figures keep a column of changing numbers from shifting.
  static const _tabular = [FontFeature.tabularFigures()];
  static const number = TextStyle(
      fontSize: 12.5,
      fontWeight: FontWeight.w600,
      color: ink,
      letterSpacing: 0.5,
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
    const shape = RoundedRectangleBorder(
        borderRadius: BorderRadius.all(Radius.circular(radiusControl)));
    return base.copyWith(
      scaffoldBackgroundColor: background,
      colorScheme: const ColorScheme.dark(
        primary: blueBright,
        secondary: cyan,
        surface: surface,
        error: bad,
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
              states.contains(WidgetState.disabled) ? idleTint : blue),
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled) ? inkFaint : Colors.white),
          elevation: const WidgetStatePropertyAll(0),
          padding: const WidgetStatePropertyAll(
              EdgeInsets.symmetric(horizontal: 18, vertical: 14)),
          shape: const WidgetStatePropertyAll(shape),
          textStyle: const WidgetStatePropertyAll(
              TextStyle(fontSize: 13, fontWeight: FontWeight.w700, letterSpacing: 0.5)),
        ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: ButtonStyle(
          backgroundColor: const WidgetStatePropertyAll(surface),
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled) ? inkFaint : ink),
          side: const WidgetStatePropertyAll(BorderSide(color: hairline)),
          padding: const WidgetStatePropertyAll(
              EdgeInsets.symmetric(horizontal: 16, vertical: 13)),
          shape: const WidgetStatePropertyAll(shape),
          textStyle: const WidgetStatePropertyAll(
              TextStyle(fontSize: 13, fontWeight: FontWeight.w600)),
        ),
      ),
      textButtonTheme: TextButtonThemeData(
        style: ButtonStyle(
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.disabled) ? inkFaint : cyan),
          textStyle: const WidgetStatePropertyAll(
              TextStyle(fontSize: 13, fontWeight: FontWeight.w600)),
          shape: const WidgetStatePropertyAll(RoundedRectangleBorder(
              borderRadius: BorderRadius.all(Radius.circular(radiusBadge)))),
        ),
      ),
      segmentedButtonTheme: SegmentedButtonThemeData(
        style: ButtonStyle(
          backgroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.selected) ? cyanTint : surface),
          foregroundColor: WidgetStateProperty.resolveWith((states) =>
              states.contains(WidgetState.selected) ? cyan : inkMuted),
          side: const WidgetStatePropertyAll(BorderSide(color: hairline)),
          textStyle: const WidgetStatePropertyAll(
              TextStyle(fontSize: 12.5, fontWeight: FontWeight.w600)),
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
            borderSide: const BorderSide(color: cyan, width: 1.5)),
        labelStyle: small,
        floatingLabelStyle: const TextStyle(
            fontSize: 12, color: cyan, fontWeight: FontWeight.w600),
        hintStyle: hint,
        prefixIconColor: inkFaint,
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: surfaceElevated,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(radiusPanel),
            side: const BorderSide(color: hairline)),
        titleTextStyle: section,
        contentTextStyle: body,
      ),
      snackBarTheme: SnackBarThemeData(
        backgroundColor: surfaceElevated,
        contentTextStyle: const TextStyle(fontSize: 13, color: ink),
        behavior: SnackBarBehavior.floating,
        shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(radiusControl),
            side: const BorderSide(color: cyan, width: 1)),
      ),
      progressIndicatorTheme:
          const ProgressIndicatorThemeData(color: cyan, linearMinHeight: 3),
    );
  }
}
