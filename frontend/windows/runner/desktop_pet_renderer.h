#ifndef RUNNER_DESKTOP_PET_RENDERER_H_
#define RUNNER_DESKTOP_PET_RENDERER_H_

#include <windows.h>
#include <gdiplus.h>

#include <cstdint>
#include <string>
#include <vector>

// Snapshot of the visual state used to deterministically render the Amiya desktop pet.
struct DesktopPetViewState {
  bool is_compact = false;
  bool image_is_atlas = true;
  float scale = 1.0f;
  bool connected = false;
  std::wstring status = L"未连接";
  std::wstring active_node;
  std::wstring mode = L"system-proxy";
  std::wstring download_rate = L"↓ 0 B/s";
  std::wstring upload_rate = L"↑ 0 B/s";

  // Animation and interaction state
  float hover_progress = 0.0f;    // 0.0f (idle) -> 1.0f (fully expanded hover card)
  bool is_hovered = false;
  int current_frame = 0;          // 0: idle tablet, 1: blink, 2: cheerful wave, 3: attentive tablet
  float breathe_offset_y = 0.0f;  // < 2.0px gentle vertical breathing

  // Speech bubble (temporarily replaces hover telemetry card)
  std::wstring speech_text;
  float speech_opacity = 0.0f;    // 0.0f -> 1.0f

  uint64_t anim_time_ms = 0;      // Monotonic tick for deterministic effects (radar, pulse)
};

// Stateless GDI+ rendering routine for the Amiya desktop pet.
void DrawDesktopPet(Gdiplus::Graphics& graphics,
                    Gdiplus::Image* image,
                    int width,
                    int height,
                    const DesktopPetViewState& state);

void DrawDesktopPet(Gdiplus::Graphics& graphics,
                    Gdiplus::Image* image,
                    const SIZE& size,
                    const DesktopPetViewState& state);

#endif  // RUNNER_DESKTOP_PET_RENDERER_H_
