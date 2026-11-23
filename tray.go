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

	usage *UsageClient // nil nếu không xác định được đường dẫn credentials

	fiveItem, weekItem, usageNoteItem  *systray.MenuItem
	usageItem, nextItem, autostartItem *systray.MenuItem
}

func (a *trayApp) onReady() {
	log := moduleLog("tray")
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	systray.SetIcon(iconData)
	systray.SetTitle("claude-keeper")

	a.fiveItem = systray.AddMenuItem("5h: --", "")
	a.fiveItem.Disable()
	a.weekItem = systray.AddMenuItem("Tuần: --", "")
	a.weekItem.Disable()
	a.usageNoteItem = systray.AddMenuItem("", "")
	a.usageNoteItem.Disable()
	a.usageNoteItem.Hide()
	a.usageItem = systray.AddMenuItem("Usage: đang tính...", "")
	a.usageItem.Disable()
	a.nextItem = systray.AddMenuItem("Gửi tiếp lúc: --:--", "")
	a.nextItem.Disable()
	systray.AddSeparator()
	refreshUsageItem := systray.AddMenuItem("Làm mới usage", "Lấy lại % usage từ server")
	pingItem := systray.AddMenuItem("Mở phiên ngay", "Gửi 1 tin để mở phiên 5 giờ mới")
	usagePageItem := systray.AddMenuItem("Mở trang Usage (claude.ai)", "")
	a.autostartItem = systray.AddMenuItemCheckbox("Tự chạy khi bật máy", "", AutostartEnabled())
	logItem := systray.AddMenuItem("Xem log", "")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Thoát", "")

	a.sched = NewScheduler(a.store, Ping, a.onSchedulerEvent)
	if a.usage != nil {
		a.sched.SetResetSource(a.usage)
	}
	go a.sched.Run(ctx)
	a.refresh()
	if a.usage != nil {
		a.applyServerUsage(a.usage.Snapshot()) // hiện ngay số từ cache
		go a.refreshServerUsage(ctx)
	}

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		usageTicker := time.NewTicker(usageAutoInterval)
		defer usageTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refresh()
			case <-usageTicker.C:
				go a.refreshServerUsage(ctx)
			case <-refreshUsageItem.ClickedCh:
				// Gọi mạng tối đa 15 giây nên phải chạy riêng; UsageClient tự chặn bấm dồn (< 60 giây).
				go a.refreshServerUsage(ctx)
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

// refreshServerUsage lấy usage từ server rồi cập nhật menu và icon.
func (a *trayApp) refreshServerUsage(ctx context.Context) {
	if a.usage == nil {
		return
	}
	a.applyServerUsage(a.usage.Refresh(ctx))
	a.refresh() // giờ "Gửi tiếp lúc" phụ thuộc giờ reset vừa lấy
}

func (a *trayApp) applyServerUsage(s UsageSnapshot) {
	a.fiveItem.SetTitle(s.FiveHourLine())
	a.weekItem.SetTitle(s.WeekLine())
	if s.Note != "" {
		a.usageNoteItem.SetTitle(s.Note)
		a.usageNoteItem.Show()
	} else {
		a.usageNoteItem.Hide()
	}
	systray.SetIcon(iconForPercent(s.FiveHourPercent()))
}

// onSchedulerEvent chạy sau mỗi lần gửi hoặc xác minh: số usage có thể vừa đổi nên cập nhật cả 2 phần menu.
func (a *trayApp) onSchedulerEvent() {
	a.refresh()
	if a.usage != nil {
		a.applyServerUsage(a.usage.Snapshot())
	}
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
