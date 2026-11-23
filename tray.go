package main

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
)

const (
	refreshInterval = time.Minute
	hoverRefreshGap = 5 * time.Second // rê chuột phát sinh sự kiện rất dày nên phải throttle
)

// trayApp gom menu khay và scheduler.
type trayApp struct {
	store  *StateStore
	sched  *Scheduler
	cancel context.CancelFunc

	usage *UsageClient // nil nếu không xác định được đường dẫn credentials

	// Làm mới khi người dùng rê chuột/mở menu: tối đa 1 lần chạy cùng lúc, cách nhau >= hoverRefreshGap.
	tipMu     sync.Mutex
	lastTip   string
	freshMu   sync.Mutex
	ctx       context.Context
	freshing  bool
	lastFresh time.Time

	fiveItem, weekItem, usageNoteItem  *systray.MenuItem
	usageItem, nextItem, autostartItem *systray.MenuItem
}

func (a *trayApp) onReady() {
	log := moduleLog("tray")
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.freshMu.Lock()
	a.ctx = ctx
	a.freshMu.Unlock()

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

	// Hook do bản systray đã vá (third_party/systray) cung cấp; chạy trên luồng UI nên phải trả về ngay.
	systray.SetOnHover(func() {
		a.updateTooltip() // tooltip hiện ngay số mới nhất đang biết; chỉ gọi Windows khi nội dung đổi
		a.requestFresh()
	})
	systray.SetOnBeforeMenu(func() {
		a.showCached() // đồng bộ, rất nhanh: menu hiện ra đã có giờ/số mới nhất đang biết
		a.requestFresh()
	})

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

// acquireFresh cho phép bắt đầu 1 lần làm mới nếu không có lần nào đang chạy và đã quá hoverRefreshGap.
func (a *trayApp) acquireFresh(now time.Time) (context.Context, bool) {
	a.freshMu.Lock()
	defer a.freshMu.Unlock()
	if a.ctx == nil || a.freshing || now.Sub(a.lastFresh) < hoverRefreshGap {
		return nil, false
	}
	a.freshing, a.lastFresh = true, now
	return a.ctx, true
}

func (a *trayApp) releaseFresh() {
	a.freshMu.Lock()
	a.freshing = false
	a.freshMu.Unlock()
}

// requestFresh không chặn: làm mới số local ngay, rồi gọi server (UsageClient tự giữ luật 60 giây/429).
func (a *trayApp) requestFresh() {
	ctx, ok := a.acquireFresh(nowFunc())
	if !ok {
		return
	}
	go func() {
		defer a.releaseFresh()
		a.refresh()
		a.refreshServerUsage(ctx)
	}()
}

// showCached cập nhật phần không cần I/O: giờ gửi tiếp và số usage server đang có.
func (a *trayApp) showCached() {
	a.updateNext()
	if a.usage != nil {
		a.applyServerUsage(a.usage.Snapshot())
	}
}

func (a *trayApp) updateNext() {
	a.nextItem.SetTitle("Gửi tiếp lúc: " + a.sched.NextPing().Format("15:04"))
	a.updateTooltip()
}

// maxTooltipRunes: đo thực tế trên Windows, tooltip bị cắt ở 64 ký tự (kể cả dấu xuống dòng) dù struct có chỗ cho 128.
// Toàn bộ chữ ở đây nằm trong BMP nên 1 rune = 1 đơn vị UTF-16.
const maxTooltipRunes = 63

// tipWindow là dòng rút gọn cho tooltip: "5h: 70% · 03:20", "Tuần: 76% · thứ 5 11:00".
func tipWindow(label string, w *UsageWindow, withWeekday bool) string {
	if w == nil {
		return label + ": --"
	}
	line := fmt.Sprintf("%s: %.0f%%", label, w.Percent)
	if !w.ResetsAt.IsZero() {
		r := w.ResetsAt.Local()
		if withWeekday {
			line += " · " + viWeekday(r.Weekday()) + " " + r.Format("15:04")
		} else {
			line += " · " + r.Format("15:04")
		}
	}
	return line
}

// buildTooltip ghép 5h, tuần và giờ gửi tiếp sao cho vừa giới hạn của Windows (dòng dài nhất ~61 ký tự).
// Số cũ thì thêm "(cũ)" ở dòng đầu; chi tiết giờ lấy số nằm ở menu.
func buildTooltip(s UsageSnapshot, next time.Time) string {
	first := tipWindow("5h", s.Usage.FiveHour, false)
	if s.Stale {
		first += " (cũ)"
	}
	tip := first + "\n" + tipWindow("Tuần", s.Usage.SevenDay, true) + "\nGửi tiếp " + next.Format("15:04")
	if r := []rune(tip); len(r) > maxTooltipRunes {
		tip = string(r[:maxTooltipRunes])
	}
	return tip
}

// updateTooltip đặt tooltip khi rê chuột; không làm gì nếu nội dung không đổi (rê chuột phát sinh sự kiện rất dày).
func (a *trayApp) updateTooltip() {
	if a.sched == nil {
		return
	}
	var s UsageSnapshot
	if a.usage != nil {
		s = a.usage.Snapshot()
	}
	tip := buildTooltip(s, a.sched.NextPing())
	a.tipMu.Lock()
	defer a.tipMu.Unlock()
	if tip == a.lastTip {
		return
	}
	a.lastTip = tip
	systray.SetTooltip(tip)
}

// sessionEnd ưu tiên giờ reset do server báo (chính xác, tính cả dùng trên web/máy khác).
// Chỉ khi chưa có thì dùng ước tính từ log local (làm tròn xuống giờ tròn nên có thể lệch) và trả estimated=true.
func (a *trayApp) sessionEnd(u SessionUsage) (end time.Time, estimated bool) {
	if a.usage != nil {
		if r := a.usage.FiveHourReset(); r.After(nowFunc()) {
			return r.Local(), false
		}
	}
	return u.End.Local(), true
}

// refresh cập nhật tooltip và 2 dòng thông tin trên menu.
func (a *trayApp) refresh() {
	a.updateNext()

	root, err := claudeProjectsDir()
	if err == nil {
		var u SessionUsage
		if u, err = ReadUsage(root, nowFunc()); err == nil {
			if u.Active {
				end, estimated := a.sessionEnd(u)
				prefix := ""
				if estimated {
					prefix = "~"
				}
				a.usageItem.SetTitle(fmt.Sprintf("Usage: %s · hết phiên lúc %s%s", formatTokens(u.Tokens), prefix, end.Format("15:04")))
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
	a.updateNext() // giờ "Gửi tiếp lúc" phụ thuộc giờ reset vừa lấy
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
	a.updateTooltip()
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
