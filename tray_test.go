package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildTooltipHasBothWindowsAndFitsWindowsLimit(t *testing.T) {
	at := time.Date(2026, 11, 26, 4, 0, 0, 0, time.Local)
	s := UsageSnapshot{
		Stale: true,
		Usage: ServerUsage{
			FetchedAt: at,
			FiveHour:  &UsageWindow{Percent: 100, ResetsAt: at},
			SevenDay:  &UsageWindow{Percent: 100, ResetsAt: at},
		},
	}
	tip := buildTooltip(s, at)
	for _, want := range []string{"5h: 100%", "Tuần: 100%", "Gửi tiếp 04:00"} {
		if !strings.Contains(tip, want) {
			t.Errorf("tooltip thiếu %q: %q", want, tip)
		}
	}
	if n := len([]rune(tip)); n > maxTooltipRunes {
		t.Errorf("tooltip dài %d > %d", n, maxTooltipRunes)
	}
	if got := buildTooltip(UsageSnapshot{}, at); !strings.HasPrefix(got, "5h: --\nTuần: --") {
		t.Errorf("chưa có số: %q", got)
	}
}

func TestAcquireFreshThrottles(t *testing.T) {
	a := &trayApp{}
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if _, ok := a.acquireFresh(t0); ok {
		t.Fatal("chưa có ctx (onReady chưa chạy) thì không được làm mới")
	}
	a.ctx = context.Background()
	if _, ok := a.acquireFresh(t0); !ok {
		t.Fatal("lần đầu phải được phép")
	}
	if _, ok := a.acquireFresh(t0.Add(time.Hour)); ok {
		t.Fatal("đang chạy thì không được chạy chồng")
	}
	a.releaseFresh()
	if _, ok := a.acquireFresh(t0.Add(hoverRefreshGap - time.Second)); ok {
		t.Fatal("chưa đủ khoảng cách tối thiểu")
	}
	if _, ok := a.acquireFresh(t0.Add(hoverRefreshGap)); !ok {
		t.Fatal("đủ khoảng cách thì phải được phép")
	}
}
