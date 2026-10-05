#ifndef RUNNER_TRAY_ICON_H_
#define RUNNER_TRAY_ICON_H_

#include <windows.h>
#include <shellapi.h>

#include <string>

// The notification-area icon. It owns nothing but the icon itself: what each
// menu choice means is decided by the window that creates it, because only that
// window can talk to the Flutter side.
class TrayIcon {
 public:
  enum MenuCommand {
    kMenuNone = 0,
    kMenuShow,
    kMenuToggleConnection,
    kMenuQuit,
  };

  TrayIcon() = default;
  ~TrayIcon();

  TrayIcon(const TrayIcon&) = delete;
  TrayIcon& operator=(const TrayIcon&) = delete;

  // Adds the icon to the notification area; |callback_message| is sent to
  // |window| for mouse events on it. Returns false when the shell refuses the
  // icon, which the caller has to respect — with no icon there would be no way
  // back to a hidden window.
  bool Create(HWND window, UINT callback_message, const std::wstring& tooltip);

  void Remove();

  // Whether the icon is registered right now. A menu click on a dead icon must
  // not be treated as "the tray is there".
  bool Present() const { return added_; }

  // Re-adds the icon after the shell has restarted, which drops every icon and
  // never tells anyone but the TaskbarCreated message.
  bool Restore();

  void SetTooltip(const std::wstring& tooltip);
  void ShowBalloon(const std::wstring& title, const std::wstring& body);
  void SetConnected(bool connected) { connected_ = connected; }

  // Pops the context menu at the cursor and reports the chosen command.
  MenuCommand ShowMenu();

 private:
  NOTIFYICONDATAW data_ = {};
  bool added_ = false;
  bool connected_ = false;
};

#endif  // RUNNER_TRAY_ICON_H_
