package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReadUsageSumsCurrentSession(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 30, 0, 0, time.UTC)
	got, err := ReadUsage(filepath.Join("testdata", "projects"), now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active {
		t.Fatal("phải có phiên đang mở")
	}
	// msg_1 = 100 (dòng trùng bị loại), msg_2 = 10; bỏ qua phiên cũ, dòng lỗi, thiếu usage, giờ sai, dòng tương lai.
	if got.Tokens != 110 {
		t.Errorf("Tokens = %d, want 110", got.Tokens)
	}
	wantStart := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if !got.Start.Equal(wantStart) || !got.End.Equal(wantStart.Add(5*time.Hour)) {
		t.Errorf("phiên = %v → %v, want %v → +5h", got.Start, got.End, wantStart)
	}
}

func TestReadUsageSessionExpired(t *testing.T) {
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC) // sau 14:00
	got, err := ReadUsage(filepath.Join("testdata", "projects"), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Tokens != 0 {
		t.Errorf("phiên đã hết hạn nhưng nhận %+v", got)
	}
}

func TestReadUsageMissingDirIsNotPanic(t *testing.T) {
	if _, err := ReadUsage(filepath.Join("testdata", "no-such-dir"), time.Now()); err != nil {
		t.Fatalf("thư mục không tồn tại phải được bỏ qua, nhận lỗi: %v", err)
	}
}

func TestFormatTokens(t *testing.T) {
	for in, want := range map[int64]string{950: "950", 12300: "12.3K", 4560000: "4.56M"} {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %s, want %s", in, got, want)
		}
	}
}
