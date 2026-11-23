package main

import (
	"context"
	"testing"
	"time"
)

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
