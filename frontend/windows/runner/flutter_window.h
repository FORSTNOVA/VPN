#ifndef RUNNER_FLUTTER_WINDOW_H_
#define RUNNER_FLUTTER_WINDOW_H_

#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <flutter/method_channel.h>
#include <flutter/standard_method_codec.h>

#include <algorithm>
#include <memory>
#include <string>
#include <vector>

#include <gdiplus.h>

#include "tray_icon.h"
#include "win32_window.h"

// A window that does nothing but host a Flutter view, plus the notification-area
// icon that keeps the window reachable once it has been hidden.
class FlutterWindow : public Win32Window {
 public:
  static constexpr int kPetBaseWidth = 220;
  static constexpr int kPetBaseHeight = 320;

  // Creates a new FlutterWindow hosting a Flutter view running |project|.
  explicit FlutterWindow(const flutter::DartProject& project);
  virtual ~FlutterWindow();

  int GetPetFullWidth() const {
    return std::clamp(static_cast<int>(kPetBaseWidth * pet_scale_), 120, 500);
  }
  int GetPetFullHeight() const {
    return std::clamp(static_cast<int>(kPetBaseHeight * pet_scale_), 180, 750);
  }

 protected:
  // Win32Window:
  bool OnCreate() override;
  void OnDestroy() override;
  LRESULT MessageHandler(HWND window, UINT const message, WPARAM const wparam,
                         LPARAM const lparam) noexcept override;

 private:
  using LifecycleChannel = flutter::MethodChannel<flutter::EncodableValue>;

  // Shows the window again after it was hidden, and tells the shell to put it
  // back in the foreground.
  void ShowFromTray();
  // Hides the window. Refuses when the tray icon is not registered, because
  // nothing would be left to bring the window back.
  void HideToTray();
  void HandleTrayEvent(WPARAM wparam, LPARAM lparam);
  void HandleTrayCommand(TrayIcon::MenuCommand command);
  // Calls a method on the Flutter side. Arguments are optional.
  void Invoke(const char* method, flutter::EncodableMap arguments = {});
  void HandleLifecycleCall(const flutter::MethodCall<flutter::EncodableValue>& call,
                           std::unique_ptr<flutter::MethodResult<flutter::EncodableValue>> result);
  bool CreatePetWindow();
  void DestroyPetWindow();
  void ConfigurePet(bool enabled, float scale, int width, int height, bool auto_mini);
  void UpdatePetPlacement();
  void UpdatePetAnimation();
  void TriggerPetClick();
  void SetPetSpeech(const std::wstring& text, uint64_t duration_ms = 4000);
  void PaintPet();
  void ShowPetContextMenu(int x, int y);
  static LRESULT CALLBACK PetWindowProc(HWND window, UINT message,
                                        WPARAM wparam, LPARAM lparam) noexcept;

  // The project to run.
  flutter::DartProject project_;

  // The Flutter instance hosted by this window.
  std::unique_ptr<flutter::FlutterViewController> flutter_controller_;

  // Talks to the Flutter side about closing, hiding and the tray's state.
  std::unique_ptr<LifecycleChannel> lifecycle_channel_;

  TrayIcon tray_;

  HWND pet_window_ = nullptr;
  ULONG_PTR gdiplus_token_ = 0;
  std::unique_ptr<Gdiplus::Image> pet_image_;
  bool pet_enabled_ = true;
  float pet_scale_ = 1.0f;
  bool pet_auto_mini_on_fullscreen_ = true;
  bool pet_compact_ = false;
  bool pet_placed_ = false;
  int pet_compact_width_ = 360;
  int pet_compact_height_ = 82;
  bool pet_connected_ = false;
  std::wstring pet_status_ = L"未连接";
  std::wstring pet_active_node_;
  std::wstring pet_mode_ = L"system-proxy";
  std::vector<std::wstring> pet_nodes_;
  std::wstring pet_download_rate_ = L"↓ 0 B/s";
  std::wstring pet_upload_rate_ = L"↑ 0 B/s";

  // Animation system state
  uint64_t pet_anim_start_tick_ = 0;
  float pet_squash_ = 0.0f;
  std::wstring pet_speech_text_;
  uint64_t pet_speech_end_tick_ = 0;
  int pet_poke_count_ = 0;
  bool pet_last_connected_ = false;
  std::wstring pet_last_node_;
  bool pet_hovered_ = false;
  bool pet_mouse_tracking_ = false;

  // A close request waits for the Flutter side to ask the user what to do, so
  // another click on the close button is ignored until that answer arrives.
  bool close_pending_ = false;
  // The "the window went to the tray" hint is worth showing once per run.
  bool balloon_shown_ = false;
};

#endif  // RUNNER_FLUTTER_WINDOW_H_
