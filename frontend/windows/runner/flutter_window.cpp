#include "flutter_window.h"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <filesystem>
#include <optional>
#include <string>
#include <vector>
#include <windowsx.h>

#include "flutter/generated_plugin_registrant.h"

namespace {

// Logical pixels: the navigation rail plus a readable detail pane. Below this
// the layout stops being usable, so the window refuses to shrink further.
constexpr int kMinWindowWidth = 900;
constexpr int kMinWindowHeight = 600;

// Sent by the shell when mouse events happen on the notification-area icon.
constexpr UINT kTrayCallbackMessage = WM_APP + 1;
// Posted to leave the message loop from a place that must not tear the engine
// down while its own callback is on the stack.
constexpr UINT kQuitMessage = WM_APP + 2;

// "TaskbarCreated": the shell broadcasts it when Explorer restarts, which drops
// every icon in the notification area. It has to be looked up at runtime.
UINT g_taskbar_created_message = 0;
constexpr wchar_t kPetWindowClass[] = L"SMARTVPN_AMIYA_DESKTOP_PET";
constexpr UINT_PTR kPetFullscreenTimer = 1;
constexpr UINT_PTR kPetAnimationTimer = 2;

enum class PetMenuCommand : UINT_PTR {
  kHeader = 1001,
  kStatusInfo,
  kSpeedInfo,
  kToggleConnect,
  kToggleMode,
  kMeasureLatency,
  kOpenMainWindow,
  kOpenPetSettings,
  kClosePet,
  kNodeBase = 2000,
};

std::wstring Utf8ToWide(const std::string& value) {
  if (value.empty()) return {};
  const int count = MultiByteToWideChar(CP_UTF8, 0, value.data(),
                                        static_cast<int>(value.size()), nullptr, 0);
  std::wstring result(count, L'\0');
  MultiByteToWideChar(CP_UTF8, 0, value.data(),
                      static_cast<int>(value.size()), result.data(), count);
  return result;
}

std::wstring FormatRate(int64_t bytes_per_second, wchar_t arrow) {
  const wchar_t* units[] = {L"B/s", L"KB/s", L"MB/s", L"GB/s"};
  double value = static_cast<double>(std::max<int64_t>(0, bytes_per_second));
  size_t unit = 0;
  while (value >= 1024 && unit < 3) {
    value /= 1024;
    ++unit;
  }
  wchar_t buffer[64]{};
  if (unit == 0) {
    swprintf_s(buffer, L"%lc %.0f %ls", arrow, value, units[unit]);
  } else {
    swprintf_s(buffer, L"%lc %.1f %ls", arrow, value, units[unit]);
  }
  return buffer;
}

void SavePetDword(const wchar_t* name, DWORD value) {
  HKEY key = nullptr;
  if (RegCreateKeyExW(HKEY_CURRENT_USER, L"Software\\SmartVPN\\DesktopPet", 0,
                      nullptr, 0, KEY_SET_VALUE, nullptr, &key, nullptr) ==
      ERROR_SUCCESS) {
    RegSetValueExW(key, name, 0, REG_DWORD,
                   reinterpret_cast<const BYTE*>(&value), sizeof(value));
    RegCloseKey(key);
  }
}

DWORD ReadPetDword(const wchar_t* name, DWORD fallback) {
  HKEY key = nullptr;
  DWORD value = fallback;
  DWORD size = sizeof(value);
  if (RegOpenKeyExW(HKEY_CURRENT_USER, L"Software\\SmartVPN\\DesktopPet", 0,
                    KEY_QUERY_VALUE, &key) == ERROR_SUCCESS) {
    RegQueryValueExW(key, name, nullptr, nullptr,
                     reinterpret_cast<BYTE*>(&value), &size);
    RegCloseKey(key);
  }
  return value;
}

bool IsDesktopOrWallpaperWindow(HWND window) {
  if (!window) return true;
  if (window == GetDesktopWindow() || window == GetShellWindow()) return true;

  // 1. Check window class name
  wchar_t class_name[256]{};
  if (GetClassNameW(window, class_name, static_cast<int>(std::size(class_name))) > 0) {
    if (_wcsicmp(class_name, L"Progman") == 0 ||
        _wcsicmp(class_name, L"WorkerW") == 0 ||
        _wcsicmp(class_name, L"Shell_TrayWnd") == 0 ||
        _wcsicmp(class_name, L"Shell_SecondaryTrayWnd") == 0 ||
        _wcsicmp(class_name, L"SHELLDLL_DefView") == 0 ||
        _wcsicmp(class_name, L"SysListView32") == 0 ||
        _wcsicmp(class_name, L"DV2ControlHost") == 0 ||
        _wcsicmp(class_name, L"Windows.UI.Core.CoreWindow") == 0) {
      return true;
    }
  }

  // 2. Check window title
  wchar_t window_text[256]{};
  if (GetWindowTextW(window, window_text, static_cast<int>(std::size(window_text))) > 0) {
    if (_wcsicmp(window_text, L"Program Manager") == 0) {
      return true;
    }
  }

  // 3. Check if window contains the desktop icon view (SHELLDLL_DefView)
  if (FindWindowExW(window, nullptr, L"SHELLDLL_DefView", nullptr) != nullptr) {
    return true;
  }

  // 4. Check root ancestor window class
  HWND root = GetAncestor(window, GA_ROOT);
  if (root && root != window) {
    wchar_t root_class[256]{};
    if (GetClassNameW(root, root_class, static_cast<int>(std::size(root_class))) > 0) {
      if (_wcsicmp(root_class, L"Progman") == 0 ||
          _wcsicmp(root_class, L"WorkerW") == 0) {
        return true;
      }
    }
  }

  // 5. Check process executable name (exclude explorer shell & known dynamic wallpaper software)
  DWORD pid = 0;
  GetWindowThreadProcessId(window, &pid);
  if (pid != 0) {
    HANDLE process = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, FALSE, pid);
    if (process) {
      wchar_t exe_path[MAX_PATH]{};
      DWORD size = MAX_PATH;
      if (QueryFullProcessImageNameW(process, 0, exe_path, &size)) {
        const wchar_t* filename = wcsrchr(exe_path, L'\\');
        filename = filename ? filename + 1 : exe_path;
        std::wstring lower_exe(filename);
        for (auto& c : lower_exe) c = towlower(c);

        // Explorer desktop/taskbar window
        if (lower_exe == L"explorer.exe") {
          // If it's explorer, only actual file explorer windows (CabinetWClass) can count as app windows.
          // Progman, WorkerW, desktop, start menu, tray are all excluded.
          if (_wcsicmp(class_name, L"CabinetWClass") != 0) {
            CloseHandle(process);
            return true;
          }
        }

        // Windows Shell experience / search components
        if (lower_exe == L"shellexperiencehost.exe" ||
            lower_exe == L"startmenuexperiencehost.exe" ||
            lower_exe == L"searchhost.exe" ||
            lower_exe == L"searchapp.exe" ||
            lower_exe == L"taskview.exe") {
          CloseHandle(process);
          return true;
        }

        // Popular live wallpaper engines and desktop widgets:
        // Wallpaper Engine (wallpaper32.exe, wallpaper64.exe, ui32.exe, webwallpaper32.exe)
        // Lively Wallpaper (lively.exe, livelywpf.exe, lively.player.*)
        // Bing Wallpaper (bingwallpaper.exe, bingwallpaperapp.exe)
        // Yuanqi Wallpaper (yuanqi.exe, yuanqidp.exe, dreamdesktop.exe)
        // UPUPOO (upupoo.exe, upupooservice.exe)
        // Rainmeter (rainmeter.exe)
        if (lower_exe.find(L"wallpaper") != std::wstring::npos ||
            lower_exe.find(L"lively") != std::wstring::npos ||
            lower_exe.find(L"yuanqi") != std::wstring::npos ||
            lower_exe.find(L"upupoo") != std::wstring::npos ||
            lower_exe.find(L"rainmeter") != std::wstring::npos ||
            lower_exe.find(L"dreamdesktop") != std::wstring::npos) {
          CloseHandle(process);
          return true;
        }
      }
      CloseHandle(process);
    }
  }

  return false;
}

bool IsPetFullscreen(HWND foreground) {
  if (!foreground || !IsWindowVisible(foreground) || IsIconic(foreground)) {
    return false;
  }
  if ((GetWindowLongPtrW(foreground, GWL_STYLE) & WS_CHILD) != 0) {
    return false;
  }
  if (IsDesktopOrWallpaperWindow(foreground)) {
    return false;
  }

  RECT window_rect{};
  if (!GetWindowRect(foreground, &window_rect)) return false;
  HMONITOR monitor = MonitorFromWindow(foreground, MONITOR_DEFAULTTONEAREST);
  MONITORINFO info{sizeof(info)};
  if (!GetMonitorInfoW(monitor, &info)) return false;
  const RECT& bounds = info.rcMonitor;
  constexpr LONG tolerance = 3;
  return window_rect.left <= bounds.left + tolerance &&
         window_rect.top <= bounds.top + tolerance &&
         window_rect.right >= bounds.right - tolerance &&
         window_rect.bottom >= bounds.bottom - tolerance;
}

// The Flutter side sends UTF-8; the shell wants UTF-16.
std::wstring Widen(const std::string& value) {
  if (value.empty()) {
    return std::wstring();
  }
  const int length = static_cast<int>(value.size());
  const int size = MultiByteToWideChar(CP_UTF8, 0, value.data(), length, nullptr, 0);
  if (size <= 0) {
    return std::wstring();
  }
  std::wstring wide(size, L'\0');
  MultiByteToWideChar(CP_UTF8, 0, value.data(), length, wide.data(), size);
  return wide;
}

}  // namespace

FlutterWindow::FlutterWindow(const flutter::DartProject& project)
    : project_(project) {}

FlutterWindow::~FlutterWindow() {}

bool FlutterWindow::CreatePetWindow() {
  pet_enabled_ = ReadPetDword(L"Enabled", 1) != 0;
  pet_auto_mini_on_fullscreen_ = ReadPetDword(L"AutoMiniOnFullscreen", 1) != 0;
  pet_scale_ = std::clamp(static_cast<float>(ReadPetDword(L"ScalePercent", 100)) / 100.0f, 0.6f, 1.8f);
  pet_compact_width_ = std::clamp<int>(ReadPetDword(L"CompactWidth", 360), 260, 560);
  pet_compact_height_ = std::clamp<int>(ReadPetDword(L"CompactHeight", 82), 68, 160);
  normal_x_ = static_cast<int>(ReadPetDword(L"NormalX", static_cast<DWORD>(-1)));
  normal_y_ = static_cast<int>(ReadPetDword(L"NormalY", static_cast<DWORD>(-1)));
  normal_position_saved_ = ReadPetDword(L"NormalPositionSaved",
      normal_x_ != -1 && normal_y_ != -1 ? 1 : 0) != 0;

  Gdiplus::GdiplusStartupInput startup_input;
  if (Gdiplus::GdiplusStartup(&gdiplus_token_, &startup_input, nullptr) !=
      Gdiplus::Ok) {
    gdiplus_token_ = 0;
  } else {
    wchar_t executable[MAX_PATH]{};
    GetModuleFileNameW(nullptr, executable, MAX_PATH);
    const auto exe_dir = std::filesystem::path(executable).parent_path();
    // Search relative / bundle paths; 2x2 sprite atlas takes priority, chibi is fallback
    std::vector<std::filesystem::path> candidates = {
        exe_dir / L"data" / L"flutter_assets" / L"assets" / L"amiya_pet_atlas.png",
        exe_dir / L"assets" / L"amiya_pet_atlas.png",
        std::filesystem::current_path() / L"assets" / L"amiya_pet_atlas.png",
        std::filesystem::current_path() / L"frontend" / L"assets" / L"amiya_pet_atlas.png",
        exe_dir / L"data" / L"flutter_assets" / L"assets" / L"amiya_chibi.png",
        exe_dir / L"assets" / L"amiya_chibi.png",
        std::filesystem::current_path() / L"assets" / L"amiya_chibi.png",
        std::filesystem::current_path() / L"frontend" / L"assets" / L"amiya_chibi.png",
    };
    for (const auto& candidate : candidates) {
      std::error_code ec;
      if (std::filesystem::exists(candidate, ec)) {
        pet_image_ = std::make_unique<Gdiplus::Image>(candidate.c_str());
        if (pet_image_->GetLastStatus() == Gdiplus::Ok) {
          pet_image_is_atlas_ = candidate.filename() == L"amiya_pet_atlas.png";
          break;
        }
        pet_image_.reset();
      }
    }
  }

  WNDCLASSW window_class{};
  window_class.style = CS_DBLCLKS;  // Support double clicks without modal delay
  window_class.hInstance = GetModuleHandleW(nullptr);
  window_class.lpfnWndProc = PetWindowProc;
  window_class.lpszClassName = kPetWindowClass;
  window_class.hCursor = LoadCursorW(nullptr, IDC_ARROW);
  window_class.hbrBackground = nullptr;
  if (!RegisterClassW(&window_class) && GetLastError() != ERROR_CLASS_ALREADY_EXISTS) {
    return false;
  }

  pet_window_ = CreateWindowExW(
      WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE | WS_EX_LAYERED,
      kPetWindowClass, L"SmartVPN · 阿米娅桌宠", WS_POPUP,
      0, 0, GetPetFullWidth(), GetPetFullHeight(), nullptr, nullptr,
      GetModuleHandleW(nullptr), this);
  if (!pet_window_) return false;

  if (pet_enabled_) {
    SetTimer(pet_window_, kPetFullscreenTimer, 500, nullptr);
    SetTimer(pet_window_, kPetAnimationTimer, 40, nullptr);
    UpdatePetPlacement();
    PaintPet();
    ShowWindow(pet_window_, SW_SHOWNOACTIVATE);
  }
  return true;
}

void FlutterWindow::DestroyPetWindow() {
  if (pet_window_) {
    KillTimer(pet_window_, kPetFullscreenTimer);
    KillTimer(pet_window_, kPetAnimationTimer);
    DestroyWindow(pet_window_);
    pet_window_ = nullptr;
  }
  pet_image_.reset();
  if (gdiplus_token_ != 0) {
    Gdiplus::GdiplusShutdown(gdiplus_token_);
    gdiplus_token_ = 0;
  }
}

void FlutterWindow::ConfigurePet(bool enabled, float scale, int width, int height, bool auto_mini) {
  const bool old_enabled = pet_enabled_;
  pet_enabled_ = enabled;
  pet_scale_ = std::clamp(scale, 0.6f, 1.8f);
  pet_compact_width_ = std::clamp(width, 260, 560);
  pet_compact_height_ = std::clamp(height, 68, 160);
  pet_auto_mini_on_fullscreen_ = auto_mini;
  SavePetDword(L"Enabled", pet_enabled_ ? 1 : 0);
  SavePetDword(L"ScalePercent", static_cast<DWORD>(std::round(pet_scale_ * 100.0f)));
  SavePetDword(L"CompactWidth", static_cast<DWORD>(pet_compact_width_));
  SavePetDword(L"CompactHeight", static_cast<DWORD>(pet_compact_height_));
  SavePetDword(L"AutoMiniOnFullscreen", pet_auto_mini_on_fullscreen_ ? 1 : 0);
  if (!pet_window_) return;
  if (pet_enabled_) {
    ShowWindow(pet_window_, SW_SHOWNOACTIVATE);
    if (!old_enabled) {
      SetTimer(pet_window_, kPetFullscreenTimer, 500, nullptr);
      SetTimer(pet_window_, kPetAnimationTimer, pet_compact_ ? 125 : 40, nullptr);
    }
    UpdatePetPlacement();
    PaintPet();
  } else {
    // Completely halt timers when disabled so no background CPU is consumed
    KillTimer(pet_window_, kPetFullscreenTimer);
    KillTimer(pet_window_, kPetAnimationTimer);
    ShowWindow(pet_window_, SW_HIDE);
  }
}

void FlutterWindow::UpdatePetPlacement() {
  if (!pet_window_ || !pet_enabled_ || is_pointer_down_ || is_dragging_) return;
  HWND foreground = GetForegroundWindow();
  DWORD foreground_pid = 0;
  if (foreground) GetWindowThreadProcessId(foreground, &foreground_pid);
  const bool compact = pet_auto_mini_on_fullscreen_ &&
      foreground_pid != GetCurrentProcessId() && IsPetFullscreen(foreground);
  const bool mode_changed = pet_compact_ != compact;
  RECT current{};
  GetWindowRect(pet_window_, &current);
  if (mode_changed && compact && pet_placed_) {
    normal_x_ = current.left;
    normal_y_ = current.top;
    normal_position_saved_ = true;
  }
  HMONITOR monitor;
  if (compact) {
    monitor = MonitorFromWindow(foreground, MONITOR_DEFAULTTONEAREST);
  } else if (normal_position_saved_) {
    monitor = MonitorFromPoint(POINT{normal_x_, normal_y_}, MONITOR_DEFAULTTONEAREST);
  } else {
    monitor = MonitorFromWindow(GetHandle(), MONITOR_DEFAULTTONEAREST);
  }
  MONITORINFO info{sizeof(info)};
  if (!GetMonitorInfoW(monitor, &info)) return;
  const RECT bounds = info.rcWork;
  const int width = compact ? pet_compact_width_ : GetPetFullWidth();
  const int height = compact ? pet_compact_height_ : GetPetFullHeight();
  const bool size_changed = current.right-current.left != width || current.bottom-current.top != height;
  int x = current.left;
  int y = current.top;
  if (compact) {
    if (mode_changed || !pet_placed_ || size_changed ||
        MonitorFromWindow(pet_window_, MONITOR_DEFAULTTONEAREST) != monitor) {
      x = bounds.right - width - 14;
      y = bounds.top + 14;
    }
  } else if (mode_changed || !pet_placed_) {
    x = normal_position_saved_ ? normal_x_ : bounds.right-width-20;
    y = normal_position_saved_ ? normal_y_ : bounds.bottom-height-40;
  }
  x = std::clamp(x, static_cast<int>(bounds.left), std::max<int>(bounds.left, bounds.right-width));
  y = std::clamp(y, static_cast<int>(bounds.top), std::max<int>(bounds.top, bounds.bottom-height));
  if (!compact) {
    normal_x_=x; normal_y_=y; normal_position_saved_=true;
  }
  pet_compact_=compact;
  if (mode_changed) {
    hover_progress_=0.0f; pet_hovered_=false; pet_hover_until_=0;
    pet_mouse_tracking_=false;
    SetTimer(pet_window_, kPetAnimationTimer, compact ? 125 : 40, nullptr);
  }
  if (mode_changed || !pet_placed_ || size_changed || x!=current.left || y!=current.top) {
    SetWindowPos(pet_window_, HWND_TOPMOST, x, y, width, height, SWP_NOACTIVATE | SWP_SHOWWINDOW);
    PaintPet();
  }
  pet_placed_=true;
}

void FlutterWindow::SetPetSpeech(const std::wstring& text, uint64_t duration_ms) {
  pet_speech_text_ = text;
  pet_speech_end_tick_ = GetTickCount64() + duration_ms;
  PaintPet();
}

void FlutterWindow::TriggerPetClick() {
  const uint64_t now = GetTickCount64();
  wave_end_tick_ = now + 1200;  // Frame 2 cheerful wave ~1.2s on intentional click
  pet_poke_count_++;

  static const wchar_t* kQuotes[] = {
      L"博士，阿米娅在这里。",
      L"需要我时，轻轻点一下就好。",
      L"移入鼠标，就能查看当前链路。",
      L"右键可切换节点或进行测速诊断哦。",
      L"工作辛苦了，博士也要注意休息。",
      L"今天也一起把工作做好吧，博士。",
  };
  constexpr size_t num_quotes = sizeof(kQuotes) / sizeof(kQuotes[0]);
  SetPetSpeech(kQuotes[(pet_poke_count_ - 1) % num_quotes], 2800);
}

void FlutterWindow::UpdatePetAnimation() {
  if (!pet_window_ || !pet_enabled_ || !IsWindowVisible(pet_window_)) return;

  if (pet_anim_start_tick_ == 0) pet_anim_start_tick_ = GetTickCount64();
  const uint64_t now = GetTickCount64();

  // 1. Smooth hover card expansion/collapse
  const float target_hover = (pet_hovered_ || now < pet_hover_until_) ? 1.0f : 0.0f;
  if (std::abs(hover_progress_ - target_hover) > 0.01f) {
    hover_progress_ += (target_hover - hover_progress_) * 0.25f;
  } else {
    hover_progress_ = target_hover;
  }

  // 2. Sprite frame sequencing:
  // Frame 2: cheerful wave (~1.2s on click)
  // Frame 3: attentive tablet (when hover telemetry panel is active)
  // Frame 1: periodic blink (~140ms every 5.5s)
  // Frame 0: neutral idle
  if (now < wave_end_tick_) {
    current_frame_ = 2;
  } else if (pet_hovered_ || hover_progress_ > 0.5f) {
    current_frame_ = 3;
  } else {
    const uint64_t blink_cycle = (now - pet_anim_start_tick_) % 5500;
    if (blink_cycle < 140) {
      current_frame_ = 1;
    } else {
      current_frame_ = 0;
    }
  }

  // 3. Gentle breathing motion (< 2px)
  breathe_offset_y_ = sinf(static_cast<float>(now - pet_anim_start_tick_) / 1000.0f * 2.2f) * 1.5f;

  // 4. Speech timer expiration
  if (pet_speech_end_tick_ > 0 && now >= pet_speech_end_tick_) {
    pet_speech_end_tick_ = 0;
    pet_speech_text_.clear();
  }

  PaintPet();
}

DesktopPetViewState FlutterWindow::BuildPetViewState() const {
  DesktopPetViewState state;
  state.is_compact = pet_compact_;
  state.image_is_atlas = pet_image_is_atlas_;
  state.scale = pet_scale_;
  state.connected = pet_connected_;
  state.status = pet_status_;
  state.active_node = pet_active_node_;
  state.mode = pet_mode_;
  state.download_rate = pet_download_rate_;
  state.upload_rate = pet_upload_rate_;

  state.hover_progress = hover_progress_;
  state.is_hovered = pet_hovered_;
  state.current_frame = current_frame_;
  state.breathe_offset_y = breathe_offset_y_;

  state.speech_text = pet_speech_text_;
  const uint64_t now = GetTickCount64();
  if (now < pet_speech_end_tick_ && !pet_speech_text_.empty()) {
    const float time_left = static_cast<float>(pet_speech_end_tick_ - now) / 1000.0f;
    state.speech_opacity = std::clamp(time_left * 2.5f, 0.0f, 1.0f);
  } else {
    state.speech_opacity = 0.0f;
  }
  state.anim_time_ms = now - pet_anim_start_tick_;
  return state;
}

void FlutterWindow::PaintPet() {
  if (!pet_window_ || !pet_enabled_) return;
  using namespace Gdiplus;
  RECT client{};
  GetClientRect(pet_window_, &client);
  const int width = client.right - client.left;
  const int height = client.bottom - client.top;
  if (width <= 0 || height <= 0) return;

  BITMAPINFO bmi{};
  bmi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
  bmi.bmiHeader.biWidth = width;
  bmi.bmiHeader.biHeight = -height;  // Top-down DIB
  bmi.bmiHeader.biPlanes = 1;
  bmi.bmiHeader.biBitCount = 32;
  bmi.bmiHeader.biCompression = BI_RGB;

  void* bits = nullptr;
  HDC screen_dc = GetDC(nullptr);
  HDC mem_dc = CreateCompatibleDC(screen_dc);
  HBITMAP mem_bitmap = CreateDIBSection(mem_dc, &bmi, DIB_RGB_COLORS, &bits, nullptr, 0);
  if (!mem_bitmap || !bits) {
    if (mem_dc) DeleteDC(mem_dc);
    if (screen_dc) ReleaseDC(nullptr, screen_dc);
    return;
  }
  HGDIOBJ old_bitmap = SelectObject(mem_dc, mem_bitmap);

  {
    Bitmap gdi_bitmap(width, height, width * 4, PixelFormat32bppPARGB, static_cast<BYTE*>(bits));
    Graphics graphics(&gdi_bitmap);
    DesktopPetViewState view_state = BuildPetViewState();
    DrawDesktopPet(graphics, pet_image_.get(), width, height, view_state);
  }

  POINT pt_src{0, 0};
  SIZE wnd_size{width, height};
  BLENDFUNCTION blend{};
  blend.BlendOp = AC_SRC_OVER;
  blend.BlendFlags = 0;
  blend.SourceConstantAlpha = 255;
  blend.AlphaFormat = AC_SRC_ALPHA;

  RECT wnd_rect{};
  GetWindowRect(pet_window_, &wnd_rect);
  POINT pt_dst{wnd_rect.left, wnd_rect.top};

  UpdateLayeredWindow(pet_window_, screen_dc, &pt_dst, &wnd_size, mem_dc, &pt_src, 0, &blend, ULW_ALPHA);

  SelectObject(mem_dc, old_bitmap);
  DeleteObject(mem_bitmap);
  DeleteDC(mem_dc);
  ReleaseDC(nullptr, screen_dc);
}

void FlutterWindow::ShowPetContextMenu(int x, int y) {
  HMENU menu = CreatePopupMenu();
  if (!menu) return;

  AppendMenuW(menu, MF_STRING | MF_DISABLED, static_cast<UINT_PTR>(PetMenuCommand::kHeader),
              L"[ PRTS · 阿米娅指挥终端 ]");

  std::wstring status_text = L"● 状态: " + pet_status_;
  if (!pet_active_node_.empty()) {
    status_text += L"  (" + pet_active_node_ + L")";
  }
  AppendMenuW(menu, MF_STRING | MF_DISABLED, static_cast<UINT_PTR>(PetMenuCommand::kStatusInfo),
              status_text.c_str());

  std::wstring rate_text = pet_download_rate_ + L"   " + pet_upload_rate_;
  AppendMenuW(menu, MF_STRING | MF_DISABLED, static_cast<UINT_PTR>(PetMenuCommand::kSpeedInfo),
              rate_text.c_str());

  AppendMenuW(menu, MF_SEPARATOR, 0, nullptr);

  // 1. Toggle connection
  std::wstring connect_label = pet_connected_ ? L"[开关连接]  断开连接" : L"[开关连接]  启动连接";
  AppendMenuW(menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kToggleConnect),
              connect_label.c_str());

  // 2. Toggle proxy mode
  std::wstring mode_label = pet_mode_ == L"tun" ? L"[代理模式]  切换为: 系统代理" : L"[代理模式]  切换为: TUN 全设备代理";
  AppendMenuW(menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kToggleMode),
              mode_label.c_str());

  // 3. Measure latency / test connection
  AppendMenuW(menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kMeasureLatency),
              L"[测速诊断]  ⚡ 立即测试连接延迟");

  // 4. Switch nodes (submenu)
  HMENU node_menu = CreatePopupMenu();
  if (pet_nodes_.empty()) {
    AppendMenuW(node_menu, MF_STRING | MF_DISABLED, 0, L"(暂无可用节点)");
  } else {
    for (size_t i = 0; i < pet_nodes_.size() && i < 40; ++i) {
      std::wstring label = pet_nodes_[i];
      if (label == pet_active_node_) {
        label = L"✔ " + label;
      }
      AppendMenuW(node_menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kNodeBase) + i,
                  label.c_str());
    }
  }
  AppendMenuW(menu, MF_POPUP, reinterpret_cast<UINT_PTR>(node_menu), L"[切换节点]  节点列表 ▶");

  AppendMenuW(menu, MF_SEPARATOR, 0, nullptr);

  // 5. Open main window
  AppendMenuW(menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kOpenMainWindow),
              L"打开 SmartVPN 主界面");

  // 6. Settings
  AppendMenuW(menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kOpenPetSettings),
              L"桌宠设置 (调整小窗尺寸 / 全屏切换)...");

  // 7. Close pet
  AppendMenuW(menu, MF_STRING, static_cast<UINT_PTR>(PetMenuCommand::kClosePet),
              L"关闭桌宠");

  SetForegroundWindow(pet_window_);
  const UINT command = TrackPopupMenuEx(
      menu, TPM_RIGHTBUTTON | TPM_RETURNCMD | TPM_NONOTIFY, x, y, pet_window_,
      nullptr);
  DestroyMenu(menu);

  if (command == 0) return;

  if (command == static_cast<UINT>(PetMenuCommand::kToggleConnect)) {
    Invoke("toggleConnection");
  } else if (command == static_cast<UINT>(PetMenuCommand::kToggleMode)) {
    Invoke("toggleMode");
  } else if (command == static_cast<UINT>(PetMenuCommand::kMeasureLatency)) {
    Invoke("measureLatency");
  } else if (command == static_cast<UINT>(PetMenuCommand::kOpenMainWindow)) {
    ShowFromTray();
  } else if (command == static_cast<UINT>(PetMenuCommand::kOpenPetSettings)) {
    ShowFromTray();
    Invoke("openPetSettings");
  } else if (command == static_cast<UINT>(PetMenuCommand::kClosePet)) {
    ConfigurePet(false, pet_scale_, pet_compact_width_, pet_compact_height_, pet_auto_mini_on_fullscreen_);
    flutter::EncodableMap args;
    args[flutter::EncodableValue("enabled")] = flutter::EncodableValue(false);
    Invoke("petToggled", args);
  } else if (command >= static_cast<UINT>(PetMenuCommand::kNodeBase)) {
    size_t index = command - static_cast<UINT>(PetMenuCommand::kNodeBase);
    if (index < pet_nodes_.size()) {
      std::string node_name_utf8;
      const int size = WideCharToMultiByte(CP_UTF8, 0, pet_nodes_[index].data(),
                                           static_cast<int>(pet_nodes_[index].size()),
                                           nullptr, 0, nullptr, nullptr);
      if (size > 0) {
        node_name_utf8.resize(size);
        WideCharToMultiByte(CP_UTF8, 0, pet_nodes_[index].data(),
                            static_cast<int>(pet_nodes_[index].size()),
                            node_name_utf8.data(), size, nullptr, nullptr);
      }
      flutter::EncodableMap args;
      args[flutter::EncodableValue("name")] = flutter::EncodableValue(node_name_utf8);
      Invoke("selectNode", args);
    }
  }
}

LRESULT CALLBACK FlutterWindow::PetWindowProc(HWND window, UINT message,
                                              WPARAM wparam,
                                              LPARAM lparam) noexcept {
  auto* owner = reinterpret_cast<FlutterWindow*>(
      GetWindowLongPtrW(window, GWLP_USERDATA));
  if (message == WM_NCCREATE) {
    const auto* create = reinterpret_cast<CREATESTRUCTW*>(lparam);
    owner = static_cast<FlutterWindow*>(create->lpCreateParams);
    SetWindowLongPtrW(window, GWLP_USERDATA,
                      reinterpret_cast<LONG_PTR>(owner));
  }
  if (!owner) return DefWindowProcW(window, message, wparam, lparam);
  switch (message) {
    case WM_TIMER:
      if (wparam == kPetFullscreenTimer) owner->UpdatePetPlacement();
      if (wparam == kPetAnimationTimer) owner->UpdatePetAnimation();
      return 0;

    case WM_MOUSEMOVE: {
      if (owner->is_pointer_down_) {
        POINT cur{};
        GetCursorPos(&cur);
        const int dx = cur.x - owner->drag_start_cursor_.x;
        const int dy = cur.y - owner->drag_start_cursor_.y;
        if (!owner->is_dragging_) {
          const int threshold_x = GetSystemMetrics(SM_CXDRAG);
          const int threshold_y = GetSystemMetrics(SM_CYDRAG);
          if (std::abs(dx) >= threshold_x || std::abs(dy) >= threshold_y) {
            owner->is_dragging_ = true;
          }
        }
        if (owner->is_dragging_) {
          int new_x = owner->drag_start_window_.x + dx;
          int new_y = owner->drag_start_window_.y + dy;

          HMONITOR mon = MonitorFromPoint(cur, MONITOR_DEFAULTTONEAREST);
          MONITORINFO mi{sizeof(mi)};
          if (GetMonitorInfoW(mon, &mi)) {
            const RECT& bounds = mi.rcWork;
            RECT wr{};
            GetWindowRect(window, &wr);
            const int w = wr.right - wr.left;
            const int h = wr.bottom - wr.top;
            if (new_x + w > bounds.right) new_x = bounds.right - w;
            if (new_y + h > bounds.bottom) new_y = bounds.bottom - h;
            if (new_x < bounds.left) new_x = bounds.left;
            if (new_y < bounds.top) new_y = bounds.top;
          }
          SetWindowPos(window, HWND_TOPMOST, new_x, new_y, 0, 0,
                       SWP_NOSIZE | SWP_NOACTIVATE);
          if (!owner->pet_compact_) {
            owner->normal_x_ = new_x;
            owner->normal_y_ = new_y;
            owner->normal_position_saved_ = true;
          }
        }
        return 0;
      }

      if (!owner->pet_mouse_tracking_) {
        TRACKMOUSEEVENT tme{sizeof(tme), TME_LEAVE, window, 0};
        TrackMouseEvent(&tme);
        owner->pet_mouse_tracking_ = true;
        owner->pet_hovered_ = true;
      }
      return 0;
    }

    case WM_MOUSELEAVE: {
      owner->pet_mouse_tracking_ = false;
      owner->pet_hovered_ = false;
      owner->pet_hover_until_ = GetTickCount64() + 350;
      return 0;
    }

    case WM_PAINT: {
      PAINTSTRUCT paint{};
      BeginPaint(window, &paint);
      owner->PaintPet();
      EndPaint(window, &paint);
      return 0;
    }

    case WM_ERASEBKGND:
      return 1;

    case WM_MOUSEACTIVATE:
      return MA_NOACTIVATE;

    case WM_NCHITTEST: {
      if (owner->is_dragging_ || owner->is_pointer_down_ || owner->pet_compact_) {
        return HTCLIENT;
      }
      POINT pt{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
      ScreenToClient(window, &pt);
      RECT cr{};
      GetClientRect(window, &cr);
      const int w = cr.right - cr.left;
      const int h = cr.bottom - cr.top;
      if (pt.x < 0 || pt.x >= w || pt.y < 0 || pt.y >= h) {
        return HTNOWHERE;
      }

      // Upper area (~100px) is interactive if hover card or speech is visible
      const bool upper_active = (owner->hover_progress_ > 0.05f) ||
                                (owner->pet_speech_end_tick_ > GetTickCount64());
      const int upper_boundary = static_cast<int>(100.0f * (static_cast<float>(w) / FlutterWindow::kPetBaseWidth));
      if (upper_active && pt.y < upper_boundary) {
        return HTCLIENT;
      }

      // Character lower area
      const int char_top = static_cast<int>(90.0f * (static_cast<float>(w) / FlutterWindow::kPetBaseWidth));
      if (pt.y >= char_top) {
        return HTCLIENT;
      }

      // Upper empty transparent pixels do not block desktop clicks when idle
      return HTTRANSPARENT;
    }

    case WM_LBUTTONDOWN: {
      owner->is_pointer_down_ = true;
      owner->is_dragging_ = false;
      GetCursorPos(&owner->drag_start_cursor_);
      RECT wr{};
      GetWindowRect(window, &wr);
      owner->drag_start_window_.x = wr.left;
      owner->drag_start_window_.y = wr.top;
      SetCapture(window);
      return 0;
    }

    case WM_LBUTTONUP: {
      if (owner->is_pointer_down_) {
        const bool was_dragging = owner->is_dragging_;
        owner->is_pointer_down_ = false;
        owner->is_dragging_ = false;
        ReleaseCapture();
        if (was_dragging) {
          if (!owner->pet_compact_) {
            SavePetDword(L"NormalX", static_cast<DWORD>(owner->normal_x_));
            SavePetDword(L"NormalY", static_cast<DWORD>(owner->normal_y_));
            SavePetDword(L"NormalPositionSaved", 1);
          }
        } else {
          // Intentional click release without drag -> trigger greeting
          owner->TriggerPetClick();
        }
        return 0;
      }
      break;
    }

    case WM_CAPTURECHANGED:
    case WM_CANCELMODE: {
      owner->is_pointer_down_ = false;
      owner->is_dragging_ = false;
      if (message == WM_CANCELMODE && GetCapture() == window) ReleaseCapture();
      return 0;
    }

    case WM_LBUTTONDBLCLK: {
      owner->is_pointer_down_ = false;
      owner->is_dragging_ = false;
      ReleaseCapture();
      owner->SetPetSpeech(L"正在为博士展开控制终端！", 2500);
      owner->ShowFromTray();
      return 0;
    }

    case WM_RBUTTONUP: {
      if (owner->is_pointer_down_) {
        owner->is_pointer_down_ = false;
        owner->is_dragging_ = false;
        ReleaseCapture();
      }
      POINT pt{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
      ClientToScreen(window, &pt);
      owner->ShowPetContextMenu(pt.x, pt.y);
      return 0;
    }

    case WM_CONTEXTMENU: {
      if (owner->is_pointer_down_) {
        owner->is_pointer_down_ = false;
        owner->is_dragging_ = false;
        ReleaseCapture();
      }
      POINT pt{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
      if (pt.x == -1 && pt.y == -1) {
        RECT r{};
        GetWindowRect(window, &r);
        pt.x = r.left + 20;
        pt.y = r.top + 20;
      }
      owner->ShowPetContextMenu(pt.x, pt.y);
      return 0;
    }
  }
  return DefWindowProcW(window, message, wparam, lparam);
}

bool FlutterWindow::OnCreate() {
  if (!Win32Window::OnCreate()) {
    return false;
  }

  RECT frame = GetClientArea();

  // The size here must match the window dimensions to avoid unnecessary surface
  // creation / destruction in the startup path.
  flutter_controller_ = std::make_unique<flutter::FlutterViewController>(
      frame.right - frame.left, frame.bottom - frame.top, project_);
  // Ensure that basic setup of the controller was successful.
  if (!flutter_controller_->engine() || !flutter_controller_->view()) {
    return false;
  }
  RegisterPlugins(flutter_controller_->engine());
  SetChildContent(flutter_controller_->view()->GetNativeWindow());

  lifecycle_channel_ = std::make_unique<LifecycleChannel>(
      flutter_controller_->engine()->messenger(), "smartvpn/lifecycle",
      &flutter::StandardMethodCodec::GetInstance());
  lifecycle_channel_->SetMethodCallHandler(
      [this](const flutter::MethodCall<flutter::EncodableValue>& call,
             std::unique_ptr<flutter::MethodResult<flutter::EncodableValue>>
                 result) { HandleLifecycleCall(call, std::move(result)); });

  CreatePetWindow();

  // Whether the tray icon exists decides whether the window may ever be hidden,
  // so the result is kept rather than assumed.
  g_taskbar_created_message = RegisterWindowMessageW(L"TaskbarCreated");
  tray_.Create(GetHandle(), kTrayCallbackMessage, L"SmartVPN · 未连接");

  flutter_controller_->engine()->SetNextFrameCallback([&]() {
    this->Show();
  });

  // Flutter can complete the first frame before the "show window" callback is
  // registered. The following call ensures a frame is pending to ensure the
  // window is shown. It is a no-op if the first frame hasn't completed yet.
  flutter_controller_->ForceRedraw();

  return true;
}

void FlutterWindow::OnDestroy() {
  // The icon must not outlive the window: it is the only way back to a hidden
  // window, and a stale one would open a menu for a window that no longer
  // exists.
  tray_.Remove();
  DestroyPetWindow();
  lifecycle_channel_.reset();
  if (flutter_controller_) {
    flutter_controller_ = nullptr;
  }

  Win32Window::OnDestroy();
}

void FlutterWindow::Invoke(const char* method, flutter::EncodableMap arguments) {
  if (!lifecycle_channel_) {
    return;
  }
  lifecycle_channel_->InvokeMethod(
      method, std::make_unique<flutter::EncodableValue>(arguments));
}

void FlutterWindow::HandleLifecycleCall(
    const flutter::MethodCall<flutter::EncodableValue>& call,
    std::unique_ptr<flutter::MethodResult<flutter::EncodableValue>> result) {
  const std::string& method = call.method_name();

  if (method == "closeChoice") {
    // The Flutter side has answered the close prompt, whatever it answered.
    close_pending_ = false;
    std::string action;
    if (const auto* arguments =
            std::get_if<flutter::EncodableMap>(call.arguments())) {
      const auto entry = arguments->find(flutter::EncodableValue("action"));
      if (entry != arguments->end()) {
        if (const auto* value = std::get_if<std::string>(&entry->second)) {
          action = *value;
        }
      }
    }
    if (action == "hide") {
      HideToTray();
    } else if (action == "quit") {
      // Tearing the engine down from inside its own method call would be
      // re-entrant, so the window is destroyed from the message loop instead.
      if (HWND window = GetHandle()) {
        PostMessage(window, kQuitMessage, 0, 0);
      }
    }
    result->Success();
    return;
  }

  if (method == "setStatus") {
    std::string tooltip;
    bool connected = false;
    if (const auto* arguments =
            std::get_if<flutter::EncodableMap>(call.arguments())) {
      const auto tooltip_entry =
          arguments->find(flutter::EncodableValue("tooltip"));
      if (tooltip_entry != arguments->end()) {
        if (const auto* value = std::get_if<std::string>(&tooltip_entry->second)) {
          tooltip = *value;
        }
      }
      const auto connected_entry =
          arguments->find(flutter::EncodableValue("connected"));
      if (connected_entry != arguments->end()) {
        if (const auto* value = std::get_if<bool>(&connected_entry->second)) {
          connected = *value;
        }
      }
    }
    if (!tooltip.empty()) {
      tray_.SetTooltip(Widen(tooltip));
    }
    tray_.SetConnected(connected);
    result->Success();
    return;
  }

  if (method == "getPetSettings") {
    flutter::EncodableMap settings;
    settings[flutter::EncodableValue("enabled")] =
        flutter::EncodableValue(pet_enabled_);
    settings[flutter::EncodableValue("scale")] =
        flutter::EncodableValue(static_cast<double>(pet_scale_));
    settings[flutter::EncodableValue("width")] =
        flutter::EncodableValue(static_cast<int32_t>(pet_compact_width_));
    settings[flutter::EncodableValue("height")] =
        flutter::EncodableValue(static_cast<int32_t>(pet_compact_height_));
    settings[flutter::EncodableValue("autoMiniOnFullscreen")] =
        flutter::EncodableValue(pet_auto_mini_on_fullscreen_);
    result->Success(flutter::EncodableValue(settings));
    return;
  }

  if (method == "configurePet") {
    bool enabled = pet_enabled_;
    float scale = pet_scale_;
    int width = pet_compact_width_;
    int height = pet_compact_height_;
    bool auto_mini = pet_auto_mini_on_fullscreen_;
    if (const auto* arguments =
            std::get_if<flutter::EncodableMap>(call.arguments())) {
      const auto enabled_item = arguments->find(flutter::EncodableValue("enabled"));
      if (enabled_item != arguments->end()) {
        if (const auto* value = std::get_if<bool>(&enabled_item->second)) enabled = *value;
      }
      const auto scale_item = arguments->find(flutter::EncodableValue("scale"));
      if (scale_item != arguments->end()) {
        if (const auto* value = std::get_if<double>(&scale_item->second)) scale = static_cast<float>(*value);
        if (const auto* value = std::get_if<int32_t>(&scale_item->second)) scale = static_cast<float>(*value);
        if (const auto* value = std::get_if<int64_t>(&scale_item->second)) scale = static_cast<float>(*value);
      }
      const auto width_item = arguments->find(flutter::EncodableValue("width"));
      if (width_item != arguments->end()) {
        if (const auto* value = std::get_if<int32_t>(&width_item->second)) width = *value;
        if (const auto* value = std::get_if<int64_t>(&width_item->second)) width = static_cast<int>(*value);
      }
      const auto height_item = arguments->find(flutter::EncodableValue("height"));
      if (height_item != arguments->end()) {
        if (const auto* value = std::get_if<int32_t>(&height_item->second)) height = *value;
        if (const auto* value = std::get_if<int64_t>(&height_item->second)) height = static_cast<int>(*value);
      }
      const auto auto_mini_item = arguments->find(flutter::EncodableValue("autoMiniOnFullscreen"));
      if (auto_mini_item != arguments->end()) {
        if (const auto* value = std::get_if<bool>(&auto_mini_item->second)) auto_mini = *value;
      }
    }
    ConfigurePet(enabled, scale, width, height, auto_mini);
    result->Success();
    return;
  }

  if (method == "setPetStatus") {
    const bool old_connected = pet_connected_;
    const std::wstring old_node = pet_active_node_;

    if (const auto* arguments =
            std::get_if<flutter::EncodableMap>(call.arguments())) {
      const auto status = arguments->find(flutter::EncodableValue("status"));
      if (status != arguments->end()) {
        if (const auto* value = std::get_if<std::string>(&status->second)) {
          pet_status_ = Utf8ToWide(*value);
        }
      }
      const auto connected = arguments->find(flutter::EncodableValue("connected"));
      if (connected != arguments->end()) {
        if (const auto* value = std::get_if<bool>(&connected->second)) {
          pet_connected_ = *value;
        }
      }
      const auto node = arguments->find(flutter::EncodableValue("node"));
      if (node != arguments->end()) {
        if (const auto* value = std::get_if<std::string>(&node->second)) {
          pet_active_node_ = Utf8ToWide(*value);
        }
      }
      const auto mode = arguments->find(flutter::EncodableValue("mode"));
      if (mode != arguments->end()) {
        if (const auto* value = std::get_if<std::string>(&mode->second)) {
          pet_mode_ = Utf8ToWide(*value);
        }
      }
      const auto nodes = arguments->find(flutter::EncodableValue("nodes"));
      if (nodes != arguments->end()) {
        if (const auto* list = std::get_if<flutter::EncodableList>(&nodes->second)) {
          pet_nodes_.clear();
          for (const auto& item : *list) {
            if (const auto* name = std::get_if<std::string>(&item)) {
              pet_nodes_.push_back(Utf8ToWide(*name));
            }
          }
        }
      }
      auto rate = [&](const char* key, wchar_t arrow) {
        const auto item = arguments->find(flutter::EncodableValue(key));
        if (item == arguments->end()) return FormatRate(0, arrow);
        if (const auto* value = std::get_if<int32_t>(&item->second)) return FormatRate(*value, arrow);
        if (const auto* value = std::get_if<int64_t>(&item->second)) return FormatRate(*value, arrow);
        if (const auto* value = std::get_if<double>(&item->second)) {
          return FormatRate(static_cast<int64_t>(*value), arrow);
        }
        return FormatRate(0, arrow);
      };
      pet_download_rate_ = rate("downloadRate", L'↓');
      pet_upload_rate_ = rate("uploadRate", L'↑');
    }

    if (!old_connected && pet_connected_) {
      std::wstring speech = L"博士，连接已建立";
      if (!pet_active_node_.empty()) speech += L" [" + pet_active_node_ + L"]";
      speech += L"！";
      SetPetSpeech(speech, 4000);
      wave_end_tick_ = GetTickCount64() + 1200;
    } else if (old_connected && !pet_connected_) {
      SetPetSpeech(L"连接已断开，需要时再叫我。", 3500);
    } else if (pet_connected_ && !pet_active_node_.empty() && old_node != pet_active_node_) {
      std::wstring speech = L"已为博士切换至节点: " + pet_active_node_;
      SetPetSpeech(speech, 3000);
      wave_end_tick_ = GetTickCount64() + 1200;
    }

    if (pet_window_) PaintPet();
    result->Success();
    return;
  }

  result->NotImplemented();
}

void FlutterWindow::ShowFromTray() {
  HWND window = GetHandle();
  if (window == nullptr) {
    return;
  }
  // A window that was minimized before being hidden would otherwise come back
  // minimized, which looks like nothing happened.
  ShowWindow(window, SW_SHOW);
  ShowWindow(window, SW_RESTORE);
  SetForegroundWindow(window);
}

void FlutterWindow::HideToTray() {
  HWND window = GetHandle();
  // Without a registered icon there would be no way back to the window, so this
  // is the one path that has to refuse rather than obey.
  if (window == nullptr || !tray_.Present()) {
    return;
  }
  if (!balloon_shown_) {
    balloon_shown_ = true;
    tray_.ShowBalloon(L"SmartVPN 仍在运行",
                      L"窗口已收进托盘，连接保持不动。左键托盘图标即可恢复窗口。");
  }
  ShowWindow(window, SW_HIDE);
}

void FlutterWindow::HandleTrayEvent(WPARAM wparam, LPARAM lparam) {
  switch (LOWORD(lparam)) {
    case WM_LBUTTONUP:
    case WM_LBUTTONDBLCLK:
      ShowFromTray();
      break;
    case WM_RBUTTONUP:
    case WM_CONTEXTMENU:
      HandleTrayCommand(tray_.ShowMenu());
      break;
    default:
      break;
  }
}

void FlutterWindow::HandleTrayCommand(TrayIcon::MenuCommand command) {
  switch (command) {
    case TrayIcon::kMenuShow:
      ShowFromTray();
      break;
    case TrayIcon::kMenuToggleConnection:
      Invoke("toggleConnection");
      break;
    case TrayIcon::kMenuQuit:
      // Quitting has to stop the local service so the Windows proxy settings are
      // restored, and only the Flutter side can do that.
      Invoke("quitRequested");
      break;
    default:
      break;
  }
}

LRESULT
FlutterWindow::MessageHandler(HWND hwnd, UINT const message,
                              WPARAM const wparam,
                              LPARAM const lparam) noexcept {
  // Give Flutter, including plugins, an opportunity to handle window messages.
  if (flutter_controller_) {
    std::optional<LRESULT> result =
        flutter_controller_->HandleTopLevelWindowProc(hwnd, message, wparam,
                                                      lparam);
    if (result) {
      return *result;
    }
  }

  if (message == kTrayCallbackMessage) {
    HandleTrayEvent(wparam, lparam);
    return 0;
  }

  if (message == kQuitMessage) {
    Destroy();
    return 0;
  }

  // An Explorer restart drops the icon and says so with this broadcast.
  if (message != 0 && message == g_taskbar_created_message) {
    tray_.Restore();
    return 0;
  }

  switch (message) {
    case WM_CLOSE:
      // Closing no longer ends the process: the Flutter side asks the user
      // whether to hide the window or to quit, and answers through the
      // lifecycle channel. Returning here keeps the window open until then.
      if (!close_pending_) {
        close_pending_ = true;
        flutter::EncodableMap arguments;
        arguments[flutter::EncodableValue("trayReady")] =
            flutter::EncodableValue(tray_.Present());
        Invoke("closeRequested", arguments);
      }
      return 0;
    case WM_FONTCHANGE:
      flutter_controller_->engine()->ReloadSystemFonts();
      break;
    case WM_GETMINMAXINFO: {
      // The track size is in physical pixels, so the logical minimum has to be
      // scaled by the window's DPI.
      const UINT dpi = GetDpiForWindow(hwnd);
      auto* info = reinterpret_cast<MINMAXINFO*>(lparam);
      info->ptMinTrackSize.x =
          std::max<LONG>(info->ptMinTrackSize.x,
                         MulDiv(kMinWindowWidth, dpi, 96));
      info->ptMinTrackSize.y =
          std::max<LONG>(info->ptMinTrackSize.y,
                         MulDiv(kMinWindowHeight, dpi, 96));
      return 0;
    }
  }

  return Win32Window::MessageHandler(hwnd, message, wparam, lparam);
}
