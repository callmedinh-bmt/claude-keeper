package main

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"fyne.io/systray"
)

const refreshInterval = time.Minute

// trayApp gom menu khay và scheduler.
type trayApp struct {
	store  *StateStore
	sched  *Scheduler
	cancel context.CancelFunc

	usageItem, nextItem, autostartItem *systray.MenuItem
}

func (a *trayApp) onReady() {
	log := moduleLog("tray")
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	systray.SetIcon(iconData)
	systray.SetTitle("claude-keeper")

	a.usageItem = systray.AddMenuItem("Usage: đang tính...", "")
	a.usageItem.Disable()
	a.nextItem = systray.AddMenuItem("Gửi tiếp lúc: --:--", "")
	a.nextItem.Disable()
	systray.AddSeparator()
	pingItem := systray.AddMenuItem("Mở phiên ngay", "Gửi 1 tin để mở phiên 5 giờ mới")
	usagePageItem := systray.AddMenuItem("Mở trang Usage (claude.ai)", "")
	a.autostartItem = systray.AddMenuItemCheckbox("Tự chạy khi bật máy", "", AutostartEnabled())
	logItem := systray.AddMenuItem("Xem log", "")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Thoát", "")

	a.sched = NewScheduler(a.store, Ping, a.refresh)
	go a.sched.Run(ctx)
	a.refresh()

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refresh()
			case <-pingItem.ClickedCh:
				// Chạy trong goroutine riêng để menu không bị đơ trong lúc chờ claude.
				go func() {
					if err := a.sched.PingNow(ctx); err != nil {
						log.Error("mở phiên thủ công thất bại", "err", err)
					}
				}()
			case <-usagePageItem.ClickedCh:
				if err := openWithShell(usageURL); err != nil {
					log.Error("không mở được trang usage", "err", err)
				}
			case <-a.autostartItem.ClickedCh:
				a.toggleAutostart()
			case <-logItem.ClickedCh:
				a.openLog()
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (a *trayApp) onExit() {
	if a.cancel != nil {
		a.cancel()
	}
}

// refresh cập nhật tooltip và 2 dòng thông tin trên menu.
func (a *trayApp) refresh() {
	next := a.sched.NextPing().Format("15:04")
	a.nextItem.SetTitle("Gửi tiếp lúc: " + next)
	systray.SetTooltip("claude-keeper · Gửi tiếp lúc " + next)

	root, err := claudeProjectsDir()
	if err == nil {
		var u SessionUsage
		if u, err = ReadUsage(root, nowFunc()); err == nil {
			if u.Active {
				a.usageItem.SetTitle(fmt.Sprintf("Usage: %s · hết phiên lúc %s", formatTokens(u.Tokens), u.End.Format("15:04")))
			} else {
				a.usageItem.SetTitle("Usage: chưa có phiên đang mở")
			}
			return
		}
	}
	moduleLog("tray").Error("không cập nhật được usage", "err", err)
	a.usageItem.SetTitle("Usage: lỗi đọc dữ liệu (xem log)")
}

func (a *trayApp) toggleAutostart() {
	want := !a.autostartItem.Checked()
	if err := SetAutostart(want); err != nil {
		moduleLog("tray").Error("đổi trạng thái tự chạy thất bại", "want", want, "err", err)
		return
	}
	if want {
		a.autostartItem.Check()
	} else {
		a.autostartItem.Uncheck()
	}
	moduleLog("tray").Info("đã đổi trạng thái tự chạy", "enabled", want)
}

func (a *trayApp) openLog() {
	path, err := logFilePath()
	if err != nil {
		moduleLog("tray").Error("không xác định được đường dẫn log", "err", err)
		return
	}
	// Dùng notepad để chắc chắn mở được file .log dù chưa có chương trình mặc định.
	cmd := exec.Command("notepad.exe", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	if err := cmd.Start(); err != nil {
		moduleLog("tray").Error("không mở được log", "path", path, "err", err)
	}
}
