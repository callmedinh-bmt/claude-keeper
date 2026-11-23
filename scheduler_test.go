package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var base = time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)

func TestNextPingTime(t *testing.T) {
	if got, want := NextPingTime(base), base.Add(5*time.Hour+time.Minute); !got.Equal(want) {
		t.Fatalf("NextPingTime = %v, want %v", got, want)
	}
	if !NextPingTime(time.Time{}).IsZero() {
		t.Fatal("chưa gửi lần nào thì phải đến hạn ngay (zero)")
	}
}

func TestIsDue(t *testing.T) {
	next := NextPingTime(base)
	cases := []struct {
		name    string
		now     time.Time
		last    time.Time
		retryAt time.Time
		want    bool
	}{
		{"chưa gửi bao giờ", base, time.Time{}, time.Time{}, true},
		{"trước hạn 1 giây", next.Add(-time.Second), base, time.Time{}, false},
		{"đúng hạn", next, base, time.Time{}, true},
		{"quá hạn (máy ngủ dậy)", next.Add(3 * time.Hour), base, time.Time{}, true},
		{"đến hạn nhưng đang chờ thử lại", next, base, next.Add(5 * time.Minute), false},
		{"qua mốc thử lại", next.Add(5 * time.Minute), base, next.Add(5 * time.Minute), true},
	}
	for _, c := range cases {
		if got := isDue(c.now, NextPingTime(c.last), c.retryAt); got != c.want {
			t.Errorf("%s: isDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNextPingFrom(t *testing.T) {
	reset := base.Add(3 * time.Hour) // phiên thật kết thúc sớm hơn lastPing + 5h
	cases := []struct {
		name        string
		last, reset time.Time
		want        time.Time
	}{
		{"không biết reset: theo lastPing", base, time.Time{}, base.Add(5*time.Hour + time.Minute)},
		{"reset sau lastPing: theo server", base, reset, reset.Add(time.Minute)},
		{"reset là phiên cũ (trước lastPing): theo lastPing", base, base.Add(-time.Hour), base.Add(5*time.Hour + time.Minute)},
		{"reset đã qua nhưng sau lastPing: đến hạn", base, base.Add(time.Minute), base.Add(2 * time.Minute)},
		{"chưa gửi bao giờ nhưng đang có phiên: chờ reset", time.Time{}, reset, reset.Add(time.Minute)},
		{"chưa gửi, không biết reset: gửi ngay", time.Time{}, time.Time{}, time.Time{}},
	}
	for _, c := range cases {
		if got := nextPingFrom(c.last, c.reset); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

type fakeSource struct {
	snap      UsageSnapshot
	reset     time.Time
	refreshes int
}

func (f *fakeSource) Refresh(context.Context) UsageSnapshot { f.refreshes++; return f.snap }
func (f *fakeSource) FiveHourReset() time.Time              { return f.reset }

// verifyFixture: đã gửi lúc base, đang chờ xác minh; now chỉnh được.
func verifyFixture(t *testing.T, src *fakeSource, now *time.Time, pings *int) *Scheduler {
	t.Helper()
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	s := NewScheduler(store, func(context.Context) error { *pings++; return nil }, nil)
	s.now = func() time.Time { return *now }
	s.SetResetSource(src)
	if err := s.PingNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyConfirmedWhenServerShowsNewReset(t *testing.T) {
	now, pings := base, 0
	src := &fakeSource{reset: base.Add(5 * time.Hour), snap: UsageSnapshot{Usage: ServerUsage{FetchedAt: base.Add(time.Minute)}}}
	s := verifyFixture(t, src, &now, &pings)
	now = base.Add(time.Minute)
	s.verifyStep(context.Background())
	if !s.verifySince.IsZero() || pings != 1 {
		t.Errorf("verifySince=%v pings=%d, want xác nhận xong và không gửi lại", s.verifySince, pings)
	}
}

func TestVerifyResendsWhileServerShowsOldResetThenGivesUp(t *testing.T) {
	now, pings := base, 0
	old := base.Add(-time.Hour) // reset của phiên cũ
	src := &fakeSource{reset: old}
	s := verifyFixture(t, src, &now, &pings)

	step := func() {
		now = now.Add(verifyGrace)
		src.snap = UsageSnapshot{Usage: ServerUsage{FetchedAt: now}} // số mới lấy, vẫn reset cũ
		s.verifyStep(context.Background())
	}
	for i := 1; i <= maxRePings; i++ {
		step()
		if pings != 1+i {
			t.Fatalf("lần kiểm tra %d: pings=%d, want %d", i, pings, 1+i)
		}
	}
	step() // hết lượt gửi lại: bỏ cuộc, không gửi thêm
	if pings != 1+maxRePings || !s.verifySince.IsZero() {
		t.Errorf("pings=%d verifySince=%v, want bỏ cuộc sau %d lần gửi lại", pings, s.verifySince, maxRePings)
	}
}

func TestVerifyDoesNotResendWithoutFreshData(t *testing.T) {
	now, pings := base, 0
	// FetchedAt trước lúc gửi: số cũ, không đủ căn cứ để kết luận phiên chưa mở.
	src := &fakeSource{reset: base.Add(-time.Hour), snap: UsageSnapshot{Usage: ServerUsage{FetchedAt: base.Add(-time.Minute)}}}
	s := verifyFixture(t, src, &now, &pings)
	now = base.Add(verifyGrace + time.Minute)
	s.verifyStep(context.Background())
	if pings != 1 || s.verifySince.IsZero() {
		t.Fatalf("pings=%d verifySince=%v, want vẫn chờ", pings, s.verifySince)
	}
	now = base.Add(verifyTimeout)
	s.verifyStep(context.Background())
	if pings != 1 || !s.verifySince.IsZero() {
		t.Errorf("hết timeout phải bỏ cuộc mà không gửi lại: pings=%d verifySince=%v", pings, s.verifySince)
	}
}

func TestVerifyUnverifiableWhenTokenExpired(t *testing.T) {
	now, pings := base, 0
	src := &fakeSource{snap: UsageSnapshot{Note: noteTokenExpired}}
	s := verifyFixture(t, src, &now, &pings)
	now = base.Add(verifyGrace)
	s.verifyStep(context.Background())
	if pings != 1 || !s.verifySince.IsZero() {
		t.Errorf("pings=%d verifySince=%v, want dừng xác minh và không gửi lại", pings, s.verifySince)
	}
}

func TestNextPingFromReturnsLocalTime(t *testing.T) {
	reset := time.Date(2026, 11, 23, 20, 20, 0, 0, time.UTC) // 03:20 ngày hôm sau ở UTC+7
	got := nextPingFrom(base, reset)
	if got.Location() != time.Local {
		t.Errorf("location = %v, want Local", got.Location())
	}
	if want := reset.Add(time.Minute).Local().Format("15:04"); got.Format("15:04") != want {
		t.Errorf("Format = %s, want %s", got.Format("15:04"), want)
	}
}

func TestSchedulerUsesServerReset(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.SetLastPing(base); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(store, nil, nil)
	s.now = func() time.Time { return base }
	reset := base.Add(2 * time.Hour)
	s.SetResetSource(&fakeSource{reset: reset})
	if got, want := s.NextPing(), reset.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("NextPing = %v, want %v", got, want)
	}
}

func TestPingNowSuccessSavesStateAndFailureSetsRetry(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	now := base
	fail := true
	s := NewScheduler(store, func(context.Context) error {
		if fail {
			return errors.New("boom")
		}
		return nil
	}, nil)
	s.now = func() time.Time { return now }

	if err := s.PingNow(context.Background()); err == nil {
		t.Fatal("mong đợi lỗi")
	}
	if !store.LastPing().IsZero() {
		t.Fatal("gửi lỗi thì không được lưu last_ping")
	}
	if got, want := s.NextPing(), now.Add(retryDelay); !got.Equal(want) {
		t.Fatalf("NextPing sau lỗi = %v, want %v", got, want)
	}

	fail = false
	if err := s.PingNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	// State phải còn sau khi "khởi động lại" (tạo store mới từ cùng file).
	if got := NewStateStore(store.path).LastPing(); !got.Equal(now) {
		t.Fatalf("last_ping sau reload = %v, want %v", got, now)
	}
}
