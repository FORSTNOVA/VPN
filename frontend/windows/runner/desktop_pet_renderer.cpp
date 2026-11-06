#include "desktop_pet_renderer.h"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <string>

namespace {

using namespace Gdiplus;

// Arknights / Rhodes Island industrial aesthetic palette
const Color kColorCharcoalBg(240, 20, 24, 30);
const Color kColorCharcoalCard(245, 24, 28, 36);
const Color kColorHairline(180, 48, 56, 70);
const Color kColorHairlineBright(220, 70, 82, 100);
const Color kColorIvory(255, 245, 246, 248);
const Color kColorMuted(200, 156, 169, 184);
const Color kColorFaint(160, 93, 107, 125);
const Color kColorAmber(255, 244, 201, 93);      // #F4C95D
const Color kColorAmberTint(60, 244, 201, 93);
const Color kColorCyan(255, 112, 216, 223);      // #70D8DF
const Color kColorCyanTint(60, 112, 216, 223);
const Color kColorOk(255, 79, 250, 165);         // Connected OK green-cyan
const Color kColorBad(255, 200, 42, 54);         // Blocked red

// Builds a cut-corner (45° chamfered) polygon path in the specified rectangle.
void AddCutCornerPath(GraphicsPath& path, const RectF& rect, REAL cut) {
  const REAL c = std::min({cut, rect.Width * 0.45f, rect.Height * 0.45f});
  path.Reset();
  if (c <= 0.5f) {
    path.AddRectangle(rect);
    return;
  }
  path.AddLine(rect.X + c, rect.Y, rect.GetRight() - c, rect.Y);
  path.AddLine(rect.GetRight() - c, rect.Y, rect.GetRight(), rect.Y + c);
  path.AddLine(rect.GetRight(), rect.Y + c, rect.GetRight(), rect.GetBottom() - c);
  path.AddLine(rect.GetRight(), rect.GetBottom() - c, rect.GetRight() - c, rect.GetBottom());
  path.AddLine(rect.GetRight() - c, rect.GetBottom(), rect.X + c, rect.GetBottom());
  path.AddLine(rect.X + c, rect.GetBottom(), rect.X, rect.GetBottom() - c);
  path.AddLine(rect.X, rect.GetBottom() - c, rect.X, rect.Y + c);
  path.AddLine(rect.X, rect.Y + c, rect.X + c, rect.Y);
  path.CloseFigure();
}

void DrawCutCornerBox(Graphics& graphics, const RectF& rect, REAL cut,
                      const Color& fill, const Color& border, REAL border_width = 1.0f) {
  GraphicsPath path;
  AddCutCornerPath(path, rect, cut);
  SolidBrush brush(fill);
  Pen pen(border, border_width);
  graphics.FillPath(&brush, &path);
  graphics.DrawPath(&pen, &path);
}

// Graceful minimal fallback terminal icon shown when sprite image is missing
void DrawFallbackTerminalIcon(Graphics& graphics, const RectF& box, REAL ui_scale, bool connected) {
  DrawCutCornerBox(graphics, box, 8.0f * ui_scale,
                   Color(230, 22, 26, 32), Color(200, 70, 82, 100), 1.2f * ui_scale);

  // Tactical Rhodes Island diamond glyph
  const REAL cx = box.X + box.Width / 2.0f;
  const REAL cy = box.Y + box.Height / 2.0f - 12.0f * ui_scale;
  const REAL dia = 22.0f * ui_scale;

  PointF diamond[4] = {
      PointF(cx, cy - dia),
      PointF(cx + dia, cy),
      PointF(cx, cy + dia),
      PointF(cx - dia, cy),
  };
  SolidBrush diamond_fill(connected ? kColorCyanTint : kColorAmberTint);
  Pen diamond_pen(connected ? kColorCyan : kColorAmber, 1.5f * ui_scale);
  graphics.FillPolygon(&diamond_fill, diamond, 4);
  graphics.DrawPolygon(&diamond_pen, diamond, 4);

  // Minimal PRTS / AMIYA text label
  Font title_font(L"Microsoft YaHei UI", 10.0f * ui_scale, FontStyleBold, UnitPixel);
  Font sub_font(L"Microsoft YaHei UI", 8.0f * ui_scale, FontStyleRegular, UnitPixel);
  SolidBrush ink_brush(kColorIvory);
  SolidBrush muted_brush(kColorMuted);

  StringFormat center_fmt;
  center_fmt.SetAlignment(StringAlignmentCenter);
  center_fmt.SetLineAlignment(StringAlignmentCenter);

  RectF title_rect(box.X, cy + dia + 4.0f * ui_scale, box.Width, 16.0f * ui_scale);
  graphics.DrawString(L"PRTS · AMIYA", -1, &title_font, title_rect, &center_fmt, &ink_brush);

  RectF sub_rect(box.X, cy + dia + 20.0f * ui_scale, box.Width, 14.0f * ui_scale);
  graphics.DrawString(L"桌面助理", -1, &sub_font, sub_rect, &center_fmt, &muted_brush);
}

// Renders the fullscreen compact mode mini-widget
void DrawCompactPet(Graphics& graphics, Image* image, REAL width, REAL height,
                    const DesktopPetViewState& state) {
  const RectF card_rect(0.5f, 0.5f, width - 1.0f, height - 1.0f);
  DrawCutCornerBox(graphics, card_rect, 6.0f, kColorCharcoalBg, kColorHairline, 1.0f);

  // Subtle top border industrial accent line
  const REAL accent_w = std::min(56.0f, width * 0.25f);
  Pen accent_pen(state.connected ? kColorCyan : kColorAmber, 2.0f);
  graphics.DrawLine(&accent_pen, 14.0f, 1.0f, 14.0f + accent_w, 1.0f);

  // Left avatar/portrait glyph
  const REAL glyph_h = std::clamp(height - 16.0f, 32.0f, 60.0f);
  const REAL glyph_w = glyph_h;
  const REAL glyph_x = 8.0f;
  const REAL glyph_y = (height - glyph_h) / 2.0f;
  const RectF glyph_rect(glyph_x, glyph_y, glyph_w, glyph_h);

  DrawCutCornerBox(graphics, glyph_rect, 4.0f, Color(240, 16, 20, 26), kColorHairlineBright, 1.0f);

  if (image && image->GetWidth() > 0 && image->GetHeight() > 0) {
    const UINT img_w = image->GetWidth();
    const UINT img_h = image->GetHeight();
    const bool is_atlas = state.image_is_atlas;
    const REAL src_w = is_atlas ? static_cast<REAL>(img_w) / 2.0f : static_cast<REAL>(img_w);
    const REAL src_h = is_atlas ? static_cast<REAL>(img_h) / 2.0f : static_cast<REAL>(img_h);

    GraphicsState g_state = graphics.Save();
    GraphicsPath clip_path;
    AddCutCornerPath(clip_path, glyph_rect, 4.0f);
    graphics.SetClip(&clip_path);

    // Face crop stays readable even in the smallest fullscreen strip.
    graphics.DrawImage(image, glyph_rect, src_w * 0.28f, src_h * 0.17f,
                       src_w * 0.44f, src_h * 0.44f, UnitPixel);
    graphics.Restore(g_state);
  } else {
    // Stylized Rhodes Island glyph inside mini-avatar
    const REAL cx = glyph_x + glyph_w / 2.0f;
    const REAL cy = glyph_y + glyph_h / 2.0f;
    const REAL r = glyph_w * 0.3f;
    PointF diamond[4] = {
        PointF(cx, cy - r),
        PointF(cx + r, cy),
        PointF(cx, cy + r),
        PointF(cx - r, cy),
    };
    SolidBrush dia_brush(state.connected ? kColorCyan : kColorAmber);
    graphics.FillPolygon(&dia_brush, diamond, 4);
  }

  // Right telemetry content area
  const REAL content_x = glyph_x + glyph_w + 10.0f;
  const REAL content_w = width - content_x - 10.0f;
  if (content_w <= 30.0f) return;

  const REAL mid_y = height / 2.0f;
  const REAL line1_y = std::max(6.0f, mid_y - 22.0f);
  const REAL line2_y = std::max(28.0f, mid_y + 2.0f);

  // Status dot
  const Color state_color = state.connected
                                ? kColorOk
                                : (state.status.find(L"阻断") != std::wstring::npos
                                       ? kColorBad
                                       : kColorAmber);
  SolidBrush dot_brush(state_color);
  graphics.FillEllipse(&dot_brush, content_x, line1_y + 4.0f, 7.0f, 7.0f);

  // Top-right mode tag
  std::wstring mode_tag = state.mode == L"tun" ? L"[TUN]" : L"[PROXY]";
  Font tag_font(L"Microsoft YaHei UI", 8.5f, FontStyleBold, UnitPixel);
  SolidBrush tag_brush(kColorCyan);
  StringFormat right_fmt;
  right_fmt.SetAlignment(StringAlignmentFar);
  right_fmt.SetLineAlignment(StringAlignmentNear);
  right_fmt.SetFormatFlags(StringFormatFlagsNoWrap);

  const REAL tag_w = 46.0f;
  const RectF tag_rect(content_x + content_w - tag_w, line1_y, tag_w, 16.0f);
  graphics.DrawString(mode_tag.c_str(), -1, &tag_font, tag_rect, &right_fmt, &tag_brush);

  // Line 1: Status + Node (clipped strictly)
  std::wstring display_status = state.status;
  if (state.connected && !state.active_node.empty()) {
    display_status += L" · " + state.active_node;
  }
  Font status_font(L"Microsoft YaHei UI", 11.0f, FontStyleBold, UnitPixel);
  SolidBrush ink_brush(kColorIvory);
  StringFormat status_fmt;
  status_fmt.SetAlignment(StringAlignmentNear);
  status_fmt.SetLineAlignment(StringAlignmentNear);
  status_fmt.SetTrimming(StringTrimmingEllipsisCharacter);
  status_fmt.SetFormatFlags(StringFormatFlagsNoWrap);

  const REAL status_w = std::max(10.0f, content_w - tag_w - 18.0f);
  const RectF status_rect(content_x + 12.0f, line1_y, status_w, 18.0f);
  graphics.DrawString(display_status.c_str(), -1, &status_font, status_rect, &status_fmt, &ink_brush);

  // Line 2: Two speed columns (Download & Upload)
  Font speed_font(L"Microsoft YaHei UI", 10.0f, FontStyleBold, UnitPixel);
  SolidBrush dl_brush(kColorCyan);
  SolidBrush ul_brush(kColorAmber);

  const REAL col_w = content_w / 2.0f;
  const RectF dl_rect(content_x, line2_y, col_w - 4.0f, 18.0f);
  const RectF ul_rect(content_x + col_w, line2_y, col_w - 2.0f, 18.0f);

  StringFormat speed_fmt;
  speed_fmt.SetAlignment(StringAlignmentNear);
  speed_fmt.SetLineAlignment(StringAlignmentNear);
  speed_fmt.SetTrimming(StringTrimmingEllipsisCharacter);
  speed_fmt.SetFormatFlags(StringFormatFlagsNoWrap);

  graphics.DrawString(state.download_rate.c_str(), -1, &speed_font, dl_rect, &speed_fmt, &dl_brush);
  graphics.DrawString(state.upload_rate.c_str(), -1, &speed_font, ul_rect, &speed_fmt, &ul_brush);
}

// Renders the character-first desktop pet in normal mode
void DrawNormalPet(Graphics& graphics, Image* image, REAL width, REAL height,
                   const DesktopPetViewState& state) {
  // Logical base width is 280px; ui_scale scales all coordinates proportionally
  const REAL ui_scale = width / 280.0f;

  // -------------------------------------------------------------------------
  // 1. Reserved Upper Area: Speech Bubble OR Hover Telemetry Card
  // -------------------------------------------------------------------------
  const REAL upper_pad_x = 12.0f * ui_scale;
  const REAL upper_w = width - upper_pad_x * 2.0f;

  if (state.speech_opacity > 0.05f && !state.speech_text.empty()) {
    // Interactive Speech Bubble (Speech temporarily replaces hover card)
    const auto speech_alpha = static_cast<BYTE>(std::clamp(static_cast<int>(255 * state.speech_opacity), 0, 255));
    const REAL bubble_h = 56.0f * ui_scale;
    const REAL bubble_y = 12.0f * ui_scale;
    const RectF bubble_rect(upper_pad_x, bubble_y, upper_w, bubble_h);

    DrawCutCornerBox(graphics, bubble_rect, 5.0f * ui_scale,
                     Color(speech_alpha, 20, 24, 30),
                     Color(speech_alpha, 244, 201, 93), 1.2f * ui_scale);

    // Left amber accent marker
    SolidBrush amber_mark(Color(speech_alpha, 244, 201, 93));
    graphics.FillRectangle(&amber_mark, upper_pad_x + 3.0f * ui_scale, bubble_y + 8.0f * ui_scale,
                           2.5f * ui_scale, bubble_h - 16.0f * ui_scale);

    // Downward-pointing triangular notch toward Amiya
    const REAL tail_top = bubble_rect.GetBottom();
    const REAL tail_cx = width / 2.0f;
    const PointF tail[3] = {
        PointF(tail_cx - 6.0f * ui_scale, tail_top),
        PointF(tail_cx, tail_top + 6.0f * ui_scale),
        PointF(tail_cx + 6.0f * ui_scale, tail_top),
    };
    SolidBrush tail_brush(Color(speech_alpha, 20, 24, 30));
    Pen tail_pen(Color(speech_alpha, 244, 201, 93), 1.2f * ui_scale);
    graphics.FillPolygon(&tail_brush, tail, 3);
    graphics.DrawLine(&tail_pen, tail[0], tail[1]);
    graphics.DrawLine(&tail_pen, tail[1], tail[2]);

    // Speech text (ivory, centered, concise Chinese copy)
    Font speech_font(L"Microsoft YaHei UI", 10.0f * ui_scale, FontStyleRegular, UnitPixel);
    SolidBrush speech_ink(Color(speech_alpha, 245, 246, 248));
    StringFormat speech_fmt;
    speech_fmt.SetAlignment(StringAlignmentNear);
    speech_fmt.SetLineAlignment(StringAlignmentCenter);
    speech_fmt.SetTrimming(StringTrimmingEllipsisCharacter);

    const RectF text_rect(upper_pad_x + 10.0f * ui_scale, bubble_y + 4.0f * ui_scale,
                          upper_w - 16.0f * ui_scale, bubble_h - 8.0f * ui_scale);
    graphics.DrawString(state.speech_text.c_str(), -1, &speech_font, text_rect, &speech_fmt, &speech_ink);

  } else if (state.hover_progress > 0.05f) {
    // Cut-corner telemetry card expanding smoothly above character on hover
    const auto hover_alpha = static_cast<BYTE>(std::clamp(static_cast<int>(245 * state.hover_progress), 0, 255));
    const REAL offset_y = (1.0f - state.hover_progress) * 8.0f * ui_scale;
    const REAL card_h = 76.0f * ui_scale;
    const REAL card_y = (12.0f * ui_scale) + offset_y;
    const RectF card_rect(upper_pad_x, card_y, upper_w, card_h);

    DrawCutCornerBox(graphics, card_rect, 5.0f * ui_scale,
                     Color(hover_alpha, 20, 24, 30),
                     Color(hover_alpha, 70, 82, 100), 1.0f * ui_scale);

    // Top subtle industrial accent line
    Pen card_accent(Color(hover_alpha, 244, 201, 93), 2.0f * ui_scale);
    graphics.DrawLine(&card_accent, upper_pad_x + 10.0f * ui_scale, card_y + 0.5f,
                      upper_pad_x + 46.0f * ui_scale, card_y + 0.5f);

    // Row 1: Header + Mode tag
    Font header_font(L"Microsoft YaHei UI", 8.5f * ui_scale, FontStyleBold, UnitPixel);
    SolidBrush amber_brush(Color(hover_alpha, 244, 201, 93));
    SolidBrush cyan_brush(Color(hover_alpha, 112, 216, 223));

    const RectF header_title_rect(upper_pad_x + 10.0f * ui_scale, card_y + 7.0f * ui_scale,
                                  upper_w * 0.6f, 14.0f * ui_scale);
    graphics.DrawString(L"PRTS // TELEMETRY", -1, &header_font, header_title_rect, nullptr, &amber_brush);

    std::wstring mode_str = state.mode == L"tun" ? L"[TUN MODE]" : L"[SYSTEM-PROXY]";
    StringFormat right_fmt;
    right_fmt.SetAlignment(StringAlignmentFar);
    right_fmt.SetLineAlignment(StringAlignmentNear);
    right_fmt.SetFormatFlags(StringFormatFlagsNoWrap);

    const RectF header_mode_rect(upper_pad_x + upper_w * 0.5f, card_y + 7.0f * ui_scale,
                                 upper_w * 0.5f - 10.0f * ui_scale, 14.0f * ui_scale);
    graphics.DrawString(mode_str.c_str(), -1, &header_font, header_mode_rect, &right_fmt, &cyan_brush);

    // Row 2: Status indicator + node name with ellipsis
    const Color dot_color = state.connected
                                ? kColorOk
                                : (state.status.find(L"阻断") != std::wstring::npos
                                       ? kColorBad
                                       : kColorAmber);
    SolidBrush dot_brush(Color(hover_alpha, dot_color.GetR(), dot_color.GetG(), dot_color.GetB()));
    graphics.FillEllipse(&dot_brush, upper_pad_x + 10.0f * ui_scale, card_y + 28.5f * ui_scale,
                         6.5f * ui_scale, 6.5f * ui_scale);

    std::wstring full_status = state.status;
    if (state.connected && !state.active_node.empty()) {
      full_status += L" · " + state.active_node;
    }
    Font status_font(L"Microsoft YaHei UI", 11.0f * ui_scale, FontStyleBold, UnitPixel);
    SolidBrush status_ink(Color(hover_alpha, 245, 246, 248));
    StringFormat status_fmt;
    status_fmt.SetAlignment(StringAlignmentNear);
    status_fmt.SetLineAlignment(StringAlignmentCenter);
    status_fmt.SetTrimming(StringTrimmingEllipsisCharacter);
    status_fmt.SetFormatFlags(StringFormatFlagsNoWrap);

    const RectF status_rect(upper_pad_x + 21.0f * ui_scale, card_y + 24.0f * ui_scale,
                            upper_w - 30.0f * ui_scale, 16.0f * ui_scale);
    graphics.DrawString(full_status.c_str(), -1, &status_font, status_rect, &status_fmt, &status_ink);

    // Row 3: Live Upload / Download rates (no fake graphs or claims)
    Font rate_font(L"Microsoft YaHei UI", 10.0f * ui_scale, FontStyleBold, UnitPixel);
    const REAL half_col = (upper_w - 20.0f * ui_scale) / 2.0f;
    const RectF dl_rect(upper_pad_x + 10.0f * ui_scale, card_y + 46.0f * ui_scale, half_col, 16.0f * ui_scale);
    const RectF ul_rect(upper_pad_x + 10.0f * ui_scale + half_col, card_y + 46.0f * ui_scale, half_col, 16.0f * ui_scale);

    StringFormat rate_fmt;
    rate_fmt.SetAlignment(StringAlignmentNear);
    rate_fmt.SetLineAlignment(StringAlignmentCenter);
    rate_fmt.SetTrimming(StringTrimmingEllipsisCharacter);
    rate_fmt.SetFormatFlags(StringFormatFlagsNoWrap);

    graphics.DrawString(state.download_rate.c_str(), -1, &rate_font, dl_rect, &rate_fmt, &cyan_brush);
    graphics.DrawString(state.upload_rate.c_str(), -1, &rate_font, ul_rect, &rate_fmt, &amber_brush);
  }

  // -------------------------------------------------------------------------
  // 2. Character Sprite: Lower Area (~260-280px tall) with Gentle Breathing
  // -------------------------------------------------------------------------
  const REAL char_w = 260.0f * ui_scale;
  const REAL char_h = 260.0f * ui_scale;
  const REAL char_x = (width - char_w) / 2.0f;
  const REAL char_y = height - char_h - (24.0f * ui_scale) + state.breathe_offset_y;
  const RectF char_rect(char_x, char_y, char_w, char_h);

  if (image && image->GetWidth() > 0 && image->GetHeight() > 0) {
    const UINT img_w = image->GetWidth();
    const UINT img_h = image->GetHeight();
    const bool is_atlas = state.image_is_atlas;

    if (is_atlas) {
      // 2x2 sprite atlas: derive cell dimensions from the decoded image.
      // Frame 0: top-left (idle tablet)
      // Frame 1: top-right (blink eyes closed)
      // Frame 2: bottom-left (cheerful wave)
      // Frame 3: bottom-right (attentive tablet)
      const REAL cell_w = static_cast<REAL>(img_w) / 2.0f;
      const REAL cell_h = static_cast<REAL>(img_h) / 2.0f;

      REAL src_x = 0.0f;
      REAL src_y = 0.0f;
      switch (state.current_frame) {
        case 1:  // blink
          src_x = cell_w;
          src_y = 0.0f;
          break;
        case 2:  // wave
          src_x = 0.0f;
          src_y = cell_h;
          break;
        case 3:  // attentive tablet
          src_x = cell_w;
          src_y = cell_h;
          break;
        case 0:  // idle
        default:
          src_x = 0.0f;
          src_y = 0.0f;
          break;
      }

      graphics.DrawImage(image, char_rect, src_x, src_y, cell_w, cell_h, UnitPixel);
    } else {
      // Graceful fallback to single chibi image (e.g. amiya_chibi.png)
      const REAL scale_fit = std::min(char_w / img_w, char_h / img_h);
      const REAL w_fit = img_w * scale_fit;
      const REAL h_fit = img_h * scale_fit;
      const RectF fit_rect(char_x + (char_w - w_fit) / 2.0f,
                           char_y + (char_h - h_fit),
                           w_fit, h_fit);
      graphics.DrawImage(image, fit_rect, 0.0f, 0.0f, static_cast<REAL>(img_w), static_cast<REAL>(img_h), UnitPixel);
    }
  } else {
    // Minimal tactical fallback terminal icon if no image asset is available
    DrawFallbackTerminalIcon(graphics, char_rect, ui_scale, state.connected);
  }

  // Tiny decorative status diamond light beside the character (replaces bottom status strip)
  const float light_cx = char_x + char_w * 0.80f;
  const float light_cy = char_y + char_h * 0.72f;

  const bool is_blocked = (state.status.find(L"阻断") != std::wstring::npos);
  const Color light_color = is_blocked ? kColorAmber : (state.connected ? kColorCyan : kColorMuted);

  // Soft ambient halo (14 * ui_scale diameter)
  const float halo_radius = 7.0f * ui_scale;
  const Color halo_color(40, light_color.GetR(), light_color.GetG(), light_color.GetB());
  SolidBrush halo_brush(halo_color);
  graphics.FillEllipse(&halo_brush, light_cx - halo_radius, light_cy - halo_radius, halo_radius * 2.0f, halo_radius * 2.0f);

  // Core diamond (6 * ui_scale total width/height)
  const float half_size = 3.0f * ui_scale;
  const PointF diamond_pts[4] = {
      PointF(light_cx, light_cy - half_size),
      PointF(light_cx + half_size, light_cy),
      PointF(light_cx, light_cy + half_size),
      PointF(light_cx - half_size, light_cy)
  };
  SolidBrush core_brush(light_color);
  graphics.FillPolygon(&core_brush, diamond_pts, 4);
}

}  // namespace

void DrawDesktopPet(Graphics& graphics, Image* image, int width, int height,
                    const DesktopPetViewState& state) {
  if (width <= 0 || height <= 0) return;

  graphics.SetSmoothingMode(SmoothingModeAntiAlias);
  graphics.SetInterpolationMode(InterpolationModeHighQualityBicubic);
  graphics.SetTextRenderingHint(TextRenderingHintAntiAlias);
  graphics.Clear(Color(0, 0, 0, 0));  // 100% transparent canvas

  const REAL w = static_cast<REAL>(width);
  const REAL h = static_cast<REAL>(height);

  if (state.is_compact) {
    DrawCompactPet(graphics, image, w, h, state);
  } else {
    DrawNormalPet(graphics, image, w, h, state);
  }
}

void DrawDesktopPet(Graphics& graphics, Image* image, const SIZE& size,
                    const DesktopPetViewState& state) {
  DrawDesktopPet(graphics, image, size.cx, size.cy, state);
}
