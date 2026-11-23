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
		if got := isDue(c.now, c.last, c.retryAt); got != c.want {
			t.Errorf("%s: isDue = %v, want %v", c.name, got, c.want)
		}
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
