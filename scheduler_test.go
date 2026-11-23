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

func TestSchedulerUsesServerReset(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.SetLastPing(base); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(store, nil, nil)
	s.now = func() time.Time { return base }
	reset := base.Add(2 * time.Hour)
	s.SetResetSource(func() time.Time { return reset })
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
