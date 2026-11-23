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
)

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
		return serverReset.Add(pingMargin)
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
	onDone func() // gọi sau mỗi lần gửi (để cập nhật menu); có thể nil
	// resetAt trả về giờ reset phiên 5h do server báo (zero = chưa biết); có thể nil.
	resetAt func() time.Time

	mu      sync.Mutex
	busy    bool
	retryAt time.Time
}

func NewScheduler(store *StateStore, ping func(context.Context) error, onDone func()) *Scheduler {
	return &Scheduler{store: store, ping: ping, now: nowFunc, onDone: onDone}
}

// SetResetSource gắn nguồn giờ reset từ server; gọi trước Run.
func (s *Scheduler) SetResetSource(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetAt = f
}

// nextLocked phải được gọi khi đang giữ s.mu.
func (s *Scheduler) nextLocked() time.Time {
	var reset time.Time
	if s.resetAt != nil {
		reset = s.resetAt()
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
		select {
		case <-ctx.Done():
			log.Info("scheduler dừng")
			return
		case <-ticker.C:
		}
	}
}

var errBusy = errors.New("đang có một lần gửi khác chạy")

// PingNow gửi ngay (dùng cho lịch tự động và nút "Mở phiên ngay").
// Mutex chỉ giữ khi đọc/ghi cờ, KHÔNG giữ trong lúc chạy claude (có thể mất vài phút).
func (s *Scheduler) PingNow(ctx context.Context) error {
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
