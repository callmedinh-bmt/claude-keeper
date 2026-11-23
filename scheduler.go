package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	sessionLength = 5 * time.Hour
	pingMargin    = time.Minute     // chờ thêm 1 phút sau khi phiên hết để chắc chắn phiên cũ đã đóng
	checkInterval = time.Minute     // chu kỳ kiểm tra của ticker
	retryDelay    = 5 * time.Minute // gửi lỗi thì 5 phút sau thử lại

	// Sau khi gửi, đối chiếu với server xem phiên mới đã hiện chưa.
	verifyGrace   = 3 * time.Minute  // server báo reset cũ quá ngần này thì coi là phiên chưa mở và gửi lại
	verifyTimeout = 10 * time.Minute // không có số mới từ server quá ngần này thì bỏ cuộc, không gửi lại
	maxRePings    = 3                // số lần gửi lại tối đa cho mỗi lần mở phiên
)

// ResetSource là nguồn giờ reset phiên 5h do server báo (UsageClient).
type ResetSource interface {
	FiveHourReset() time.Time // zero nếu chưa biết
	Refresh(ctx context.Context) UsageSnapshot
}

// NextPingTime là giờ sớm nhất được gửi tiếp: lastPing + 5h + 1 phút.
// lastPing zero (chưa gửi bao giờ) thì trả về zero = đến hạn ngay.
func NextPingTime(lastPing time.Time) time.Time {
	if lastPing.IsZero() {
		return time.Time{}
	}
	return lastPing.Add(sessionLength + pingMargin)
}

// nextPingFrom chọn giờ gửi tiếp. Ưu tiên giờ reset thật của phiên 5h do server báo, vì app không biết
// phiên bắt đầu từ lúc nào (có thể bạn đã dùng Claude trước lần gửi của app). serverReset chỉ đáng tin
// khi sau lastPing: nếu trước đó thì đó là phiên cũ, quay về lastPing + 5h + 1 phút.
func nextPingFrom(lastPing, serverReset time.Time) time.Time {
	if !serverReset.IsZero() && serverReset.After(lastPing) {
		return serverReset.Add(pingMargin).Local() // server trả UTC; menu dùng Format nên phải đổi sang giờ máy
	}
	return NextPingTime(lastPing)
}

// isDue cho biết đã đến lúc gửi chưa (next zero = đến hạn ngay), có tính cả mốc thử lại sau lỗi.
func isDue(now, next, retryAt time.Time) bool {
	return !now.Before(next) && !now.Before(retryAt)
}

// nowFunc bỏ monotonic clock (Round(0)): sau khi máy ngủ, so sánh theo wall clock mới đúng.
func nowFunc() time.Time { return time.Now().Round(0) }

// Scheduler gửi tin theo lịch; mọi trường đều được bảo vệ bằng mu.
type Scheduler struct {
	store  *StateStore
	ping   func(context.Context) error
	now    func() time.Time
	onDone func()      // gọi sau mỗi lần gửi (để cập nhật menu); có thể nil
	reset  ResetSource // có thể nil: khi đó chỉ tính theo lastPing, không xác minh

	mu          sync.Mutex
	busy        bool
	retryAt     time.Time
	verifySince time.Time // giờ bắt đầu lần gửi đang chờ server xác nhận (zero = không chờ)
	rePings     int       // số lần đã gửi lại trong đợt xác minh hiện tại
}

func NewScheduler(store *StateStore, ping func(context.Context) error, onDone func()) *Scheduler {
	return &Scheduler{store: store, ping: ping, now: nowFunc, onDone: onDone}
}

// SetResetSource gắn nguồn giờ reset từ server; gọi trước Run.
func (s *Scheduler) SetResetSource(src ResetSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset = src
}

// nextLocked phải được gọi khi đang giữ s.mu.
func (s *Scheduler) nextLocked() time.Time {
	var reset time.Time
	if s.reset != nil {
		reset = s.reset.FiveHourReset()
	}
	return nextPingFrom(s.store.LastPing(), reset)
}

// NextPing trả về giờ sẽ gửi tiếp (đã tính mốc thử lại nếu vừa lỗi).
func (s *Scheduler) NextPing() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.nextLocked()
	if s.retryAt.After(next) {
		return s.retryAt
	}
	if next.IsZero() {
		return s.now()
	}
	return next
}

// Run kiểm tra mỗi phút cho tới khi ctx bị huỷ. Dùng ticker + so sánh giờ thật
// (không dùng timer 5 tiếng) để máy ngủ dậy vẫn gửi đúng lúc.
func (s *Scheduler) Run(ctx context.Context) {
	log := moduleLog("scheduler")
	log.Info("scheduler bắt đầu", "last_ping", s.store.LastPing().Format(time.RFC3339))
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		due := isDue(s.now(), s.nextLocked(), s.retryAt)
		s.mu.Unlock()
		if due {
			if err := s.PingNow(ctx); err != nil && !errors.Is(err, errBusy) {
				log.Error("gửi tự động thất bại, sẽ thử lại", "retry_in", retryDelay.String(), "err", err)
			}
		}
		s.verifyStep(ctx)
		select {
		case <-ctx.Done():
			log.Info("scheduler dừng")
			return
		case <-ticker.C:
		}
	}
}

var errBusy = errors.New("đang có một lần gửi khác chạy")

// verifyStep đối chiếu lần gửi gần nhất với server: phiên mới phải hiện ra (reset sau giờ gửi).
// Server vẫn báo reset cũ sau verifyGrace thì gửi lại (tối đa maxRePings lần). Không đọc được usage
// (token hết hạn, API lỗi) thì không gửi lại vì không biết phiên đã mở hay chưa.
func (s *Scheduler) verifyStep(ctx context.Context) {
	log := moduleLog("scheduler")
	s.mu.Lock()
	src, since, busy := s.reset, s.verifySince, s.busy
	s.mu.Unlock()
	if src == nil || since.IsZero() || busy {
		return
	}

	snap := src.Refresh(ctx) // có giới hạn 60 giây/lần bên trong, gọi mỗi phút vẫn an toàn
	reset := src.FiveHourReset()
	now := s.now()
	fresh := snap.Usage.FetchedAt.After(since) // số lấy sau lúc gửi mới có giá trị xác minh
	elapsed := now.Sub(since)

	const (
		wait = iota
		confirmed
		unverifiable
		giveUp
		resend
	)
	s.mu.Lock()
	if s.verifySince != since { // có lần gửi khác chen vào trong lúc gọi mạng
		s.mu.Unlock()
		return
	}
	action := wait
	switch {
	case fresh && reset.After(since):
		action = confirmed
	case snap.Note == noteTokenExpired || snap.Note == noteNeedLogin:
		action = unverifiable
	case elapsed >= verifyTimeout || (fresh && elapsed >= verifyGrace && s.rePings >= maxRePings):
		action = giveUp
	case fresh && elapsed >= verifyGrace && !now.Before(s.retryAt):
		action = resend
		s.rePings++
	}
	attempt := s.rePings
	if action != wait && action != resend {
		s.verifySince, s.rePings = time.Time{}, 0
	}
	s.mu.Unlock()

	switch action {
	case confirmed:
		log.Info("server đã báo phiên mới", "reset", reset.Format(time.RFC3339))
	case unverifiable:
		log.Warn("không xác minh được phiên mới vì không đọc được usage", "note", snap.Note)
	case giveUp:
		log.Error("server vẫn chưa báo phiên mới, bỏ cuộc", "since", since.Format(time.RFC3339), "fresh", fresh)
	case resend:
		log.Warn("server vẫn báo reset cũ, gửi lại", "attempt", attempt, "max", maxRePings, "old_reset", reset.Format(time.RFC3339))
		if err := s.doPing(ctx, true); err != nil && !errors.Is(err, errBusy) {
			log.Error("gửi lại thất bại", "attempt", attempt, "err", err)
		}
	}
	if s.onDone != nil {
		s.onDone() // cập nhật menu và icon theo số vừa lấy
	}
}

// PingNow gửi ngay (dùng cho lịch tự động và nút "Mở phiên ngay").
func (s *Scheduler) PingNow(ctx context.Context) error { return s.doPing(ctx, false) }

// doPing gửi 1 tin. retry=true khi gửi lại trong đợt xác minh (giữ nguyên bộ đếm rePings).
// Mutex chỉ giữ khi đọc/ghi cờ, KHÔNG giữ trong lúc chạy claude (có thể mất vài phút).
func (s *Scheduler) doPing(ctx context.Context, retry bool) error {
	log := moduleLog("scheduler")
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return errBusy
	}
	s.busy = true
	s.mu.Unlock()

	started := s.now()
	err := s.ping(ctx)

	s.mu.Lock()
	s.busy = false
	if err != nil {
		s.retryAt = s.now().Add(retryDelay)
	} else {
		s.retryAt = time.Time{}
		if s.reset != nil {
			s.verifySince = started // chờ server xác nhận phiên mới
		}
		if !retry {
			s.rePings = 0
		}
	}
	s.mu.Unlock()

	if err == nil {
		// Phiên bắt đầu từ lúc gửi nên lưu giờ bắt đầu gửi.
		if saveErr := s.store.SetLastPing(started); saveErr != nil {
			log.Error("gửi được nhưng không lưu được state", "err", saveErr)
		}
		log.Info("đã mở phiên mới", "next_ping", NextPingTime(started).Format(time.RFC3339))
	}
	if s.onDone != nil {
		s.onDone()
	}
	return err
}
