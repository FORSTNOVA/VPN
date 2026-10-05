#include "tray_icon.h"

#include "resource.h"

namespace {

constexpr UINT kIconId = 1;

// The hide confirmation is informational, and a chime for it would be noise.
constexpr DWORD kBalloonFlags = NIIF_INFO | NIIF_NOSOUND;

}  // namespace

TrayIcon::~TrayIcon() { Remove(); }

bool TrayIcon::Create(HWND window, UINT callback_message,
                      const std::wstring& tooltip) {
  data_ = {};
  data_.cbSize = sizeof(data_);
  data_.hWnd = window;
  data_.uID = kIconId;
  data_.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP;
  data_.uCallbackMessage = callback_message;
  // The icon resource the window already shows, so the tray needs no extra
  // file. The shell scales it to the notification area.
  data_.hIcon = LoadIcon(GetModuleHandle(nullptr), MAKEINTRESOURCE(IDI_APP_ICON));
  if (data_.hIcon == nullptr) {
    return false;
  }
  wcsncpy_s(data_.szTip, tooltip.c_str(), _TRUNCATE);
  added_ = Shell_NotifyIconW(NIM_ADD, &data_) != FALSE;
  return added_;
}

void TrayIcon::Remove() {
  if (!added_) {
    return;
  }
  Shell_NotifyIconW(NIM_DELETE, &data_);
  added_ = false;
}

bool TrayIcon::Restore() {
  if (data_.hWnd == nullptr) {
    return false;
  }
  // The shell forgot the icon along with the rest of the notification area, so
  // this is an add rather than a modify.
  added_ = Shell_NotifyIconW(NIM_ADD, &data_) != FALSE;
  return added_;
}

void TrayIcon::SetTooltip(const std::wstring& tooltip) {
  wcsncpy_s(data_.szTip, tooltip.c_str(), _TRUNCATE);
  if (!added_) {
    return;
  }
  NOTIFYICONDATAW update = data_;
  update.uFlags = NIF_TIP;
  Shell_NotifyIconW(NIM_MODIFY, &update);
}

void TrayIcon::ShowBalloon(const std::wstring& title,
                           const std::wstring& body) {
  if (!added_) {
    return;
  }
  NOTIFYICONDATAW update = data_;
  update.uFlags = NIF_INFO;
  update.dwInfoFlags = kBalloonFlags;
  wcsncpy_s(update.szInfoTitle, title.c_str(), _TRUNCATE);
  wcsncpy_s(update.szInfo, body.c_str(), _TRUNCATE);
  Shell_NotifyIconW(NIM_MODIFY, &update);
}

TrayIcon::MenuCommand TrayIcon::ShowMenu() {
  HMENU menu = CreatePopupMenu();
  if (menu == nullptr) {
    return kMenuNone;
  }
  AppendMenuW(menu, MF_STRING, kMenuShow, L"显示窗口");
  AppendMenuW(menu, MF_STRING, kMenuToggleConnection,
              connected_ ? L"断开连接" : L"连接");
  AppendMenuW(menu, MF_SEPARATOR, 0, nullptr);
  AppendMenuW(menu, MF_STRING, kMenuQuit, L"退出 SmartVPN");

  POINT cursor;
  GetCursorPos(&cursor);
  // The popup only closes when the user clicks elsewhere if its owner is the
  // foreground window; without this the menu can be left on screen.
  SetForegroundWindow(data_.hWnd);
  const UINT chosen =
      TrackPopupMenu(menu, TPM_RETURNCMD | TPM_NONOTIFY | TPM_RIGHTBUTTON,
                     cursor.x, cursor.y, 0, data_.hWnd, nullptr);
  // A message posted after the popup is the documented way to make the shell
  // drop the menu for good.
  PostMessage(data_.hWnd, WM_NULL, 0, 0);
  DestroyMenu(menu);

  switch (chosen) {
    case kMenuShow:
      return kMenuShow;
    case kMenuToggleConnection:
      return kMenuToggleConnection;
    case kMenuQuit:
      return kMenuQuit;
    default:
      return kMenuNone;
  }
}
