package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testToken     = "FAKE-TOKEN-FOR-TEST"
	fullJSON      = "testdata/usage/full.json"
	validCredPath = "testdata/usage/credentials_valid.json"
)

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseServerUsageFull(t *testing.T) {
	u, err := ParseServerUsage(mustRead(t, fullJSON))
	if err != nil {
		t.Fatal(err)
	}
	if u.FiveHour == nil || u.FiveHour.Percent != 64 || u.FiveHour.ResetsAt.IsZero() {
		t.Errorf("five_hour = %+v", u.FiveHour)
	}
	if u.SevenDay == nil || u.SevenDay.Percent != 76 {
		t.Errorf("seven_day = %+v", u.SevenDay)
	}
}

func TestParseServerUsageMissingFields(t *testing.T) {
	u, err := ParseServerUsage(mustRead(t, "testdata/usage/missing_fields.json"))
	if err != nil {
		t.Fatal(err)
	}
	if u.FiveHour != nil {
		t.Errorf("thiếu utilization thì five_hour phải nil, nhận %+v", u.FiveHour)
	}
	if u.SevenDay == nil || u.SevenDay.Percent != 12 || !u.SevenDay.ResetsAt.IsZero() {
		t.Errorf("seven_day = %+v", u.SevenDay)
	}
}

func TestParseServerUsageWrongTypes(t *testing.T) {
	u, err := ParseServerUsage(mustRead(t, "testdata/usage/wrong_types.json"))
	if err != nil {
		t.Fatal(err)
	}
	if u.FiveHour != nil {
		t.Errorf("utilization dạng chuỗi phải bị bỏ, nhận %+v", u.FiveHour)
	}
	if u.SevenDay == nil || u.SevenDay.Percent != 12.5 || !u.SevenDay.ResetsAt.IsZero() {
		t.Errorf("seven_day = %+v", u.SevenDay)
	}
}

func TestParseServerUsageUnusable(t *testing.T) {
	for name, in := range map[string]string{
		"rỗng": "", "không phải JSON": "<html>", "mảng": "[]", "null": "null", "object rỗng": "{}",
		"cả hai null": `{"five_hour":null,"seven_day":null}`, "âm": `{"five_hour":{"utilization":-1}}`,
	} {
		if _, err := ParseServerUsage([]byte(in)); err == nil {
			t.Errorf("%s: phải trả lỗi", name)
		}
	}
}

func TestReadAccessToken(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if tok, err := readAccessToken(validCredPath, now); err != nil || tok != testToken {
		t.Errorf("token hợp lệ: tok=%q err=%v", tok, err)
	}
	if _, err := readAccessToken("testdata/usage/credentials_expired.json", now); err != errTokenExpired {
		t.Errorf("hết hạn: err=%v", err)
	}
	if _, err := readAccessToken("testdata/usage/no-such.json", now); err != errNoCredentials {
		t.Errorf("không có file: err=%v", err)
	}
	bad := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(bad, []byte("{FAKE-TOKEN-FOR-TEST"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readAccessToken(bad, now)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("file hỏng phải báo lỗi và không lộ nội dung: %v", err)
	}
}

// newTestClient dựng client trỏ vào server giả; now có thể chỉnh để mô phỏng thời gian trôi.
func newTestClient(t *testing.T, srv *httptest.Server, now *time.Time) *UsageClient {
	t.Helper()
	c := NewUsageClient(validCredPath, filepath.Join(t.TempDir(), "usage.json"))
	c.url = srv.URL
	c.now = func() time.Time { return *now }
	return c
}

func TestRefreshSuccessSendsHeadersAndCaches(t *testing.T) {
	var gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		_, _ = w.Write(mustRead(t, fullJSON))
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, srv, &now)

	s := c.Refresh(context.Background())
	if gotAuth != "Bearer "+testToken || gotBeta != usageAPIBeta {
		t.Errorf("header sai: %q / %q", gotAuth, gotBeta)
	}
	if s.Stale || s.Note != "" || s.Usage.FiveHour == nil || s.Usage.FiveHour.Percent != 64 {
		t.Errorf("snapshot = %+v", s)
	}
	cache := string(mustRead(t, c.cachePath))
	if strings.Contains(cache, testToken) || !strings.Contains(cache, "utilization") {
		t.Errorf("cache không đúng: %s", cache)
	}
	// Khởi động lại: đọc được cache và đánh dấu là số cũ.
	again := NewUsageClient(validCredPath, c.cachePath).Snapshot()
	if !again.Stale || again.Usage.SevenDay == nil || again.Usage.SevenDay.Percent != 76 {
		t.Errorf("nạp cache = %+v", again)
	}
}

func TestRefreshMinGapBetweenCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(mustRead(t, fullJSON))
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, srv, &now)

	c.Refresh(context.Background())
	now = now.Add(30 * time.Second)
	c.Refresh(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("bấm dồn trong 60 giây phải chỉ gọi 1 lần, gọi %d", calls.Load())
	}
	now = now.Add(31 * time.Second)
	c.Refresh(context.Background())
	if calls.Load() != 2 {
		t.Errorf("sau 60 giây phải gọi lại, tổng %d", calls.Load())
	}
}

func TestRefreshExpiredTokenDoesNotCallAPI(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, srv, &now)
	c.credPath = "testdata/usage/credentials_expired.json"

	if s := c.Refresh(context.Background()); s.Note != noteTokenExpired || calls.Load() != 0 {
		t.Errorf("note=%q calls=%d", s.Note, calls.Load())
	}
}

func TestRefreshErrorsKeepOldNumbers(t *testing.T) {
	cases := map[string]struct {
		status   int
		wantNote string
	}{
		"401": {http.StatusUnauthorized, noteNeedLogin},
		"403": {http.StatusForbidden, noteNeedLogin},
		"500": {http.StatusInternalServerError, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fail := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fail {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write(mustRead(t, fullJSON))
			}))
			defer srv.Close()
			now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local)
			c := newTestClient(t, srv, &now)
			c.Refresh(context.Background())

			fail = true
			now = now.Add(10 * time.Minute)
			s := c.Refresh(context.Background())
			if !s.Stale || s.Note != tc.wantNote || s.Usage.FiveHour == nil || s.Usage.FiveHour.Percent != 64 {
				t.Errorf("snapshot = %+v", s)
			}
			if line := s.FiveHourLine(); !strings.HasSuffix(line, "(cũ, lúc 12:00)") {
				t.Errorf("thiếu ghi chú số cũ: %q", line)
			}
		})
	}
}

func TestRefresh429BacksOffThirtyMinutes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, srv, &now)

	c.Refresh(context.Background())
	now = now.Add(29 * time.Minute)
	c.Refresh(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("đang nghỉ 30 phút mà vẫn gọi: %d lần", calls.Load())
	}
	now = now.Add(2 * time.Minute)
	c.Refresh(context.Background())
	if calls.Load() != 2 {
		t.Errorf("hết 30 phút phải gọi lại, tổng %d", calls.Load())
	}
}

func TestRefreshDoesNotFollowRedirect(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	srv := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer srv.Close()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, srv, &now)

	c.Refresh(context.Background())
	if leaked.Load() {
		t.Error("không được theo redirect khi đang mang token")
	}
}

func TestLines(t *testing.T) {
	s := UsageSnapshot{Usage: ServerUsage{
		FiveHour: &UsageWindow{Percent: 34, ResetsAt: time.Date(2026, 11, 23, 15, 4, 0, 0, time.Local)},
		SevenDay: &UsageWindow{Percent: 12, ResetsAt: time.Date(2026, 11, 26, 4, 0, 0, 0, time.Local)}, // thứ Năm
	}}
	if got, want := s.FiveHourLine(), "5h: 34% · reset 15:04"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := s.WeekLine(), "Tuần: 12% · reset thứ 5 04:00"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := (UsageSnapshot{}).FiveHourLine(); got != "5h: --" {
		t.Errorf("chưa có số: %q", got)
	}
	if viWeekday(time.Sunday) != "CN" || viWeekday(time.Monday) != "thứ 2" {
		t.Error("tên thứ sai")
	}
}

func TestLevelFor(t *testing.T) {
	for _, tc := range []struct {
		p    float64
		ok   bool
		want usageLevel
	}{
		{0, false, levelUnknown}, {0, true, levelGreen}, {49.9, true, levelGreen},
		{50, true, levelYellow}, {80, true, levelYellow}, {80.1, true, levelRed}, {100, true, levelRed},
	} {
		if got := levelFor(tc.p, tc.ok); got != tc.want {
			t.Errorf("levelFor(%v,%v) = %v, want %v", tc.p, tc.ok, got, tc.want)
		}
	}
}

func TestIconsAreValidAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range []float64{10, 60, 95} {
		ic := iconForPercent(p, true)
		if len(ic) < 22 || ic[2] != 1 || ic[3] != 0 {
			t.Fatalf("header ICO sai ở %v%%", p)
		}
		seen[string(ic)] = true
	}
	if len(seen) != 3 {
		t.Error("3 mức màu phải cho 3 icon khác nhau")
	}
}
