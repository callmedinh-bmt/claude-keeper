package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// API nội bộ, không có tài liệu chính thức: có thể đổi hoặc hỏng bất cứ lúc nào.
const (
	usageAPIURL       = "https://api.anthropic.com/api/oauth/usage"
	usageAPIBeta      = "oauth-2025-04-20"
	usageAPITimeout   = 15 * time.Second
	usageAutoInterval = 10 * time.Minute
	usageMinGap       = 60 * time.Second // khoảng cách tối thiểu giữa 2 lần gọi
	usageBackoff429   = 30 * time.Minute
	maxUsageBodyBytes = 1 << 20

	noteTokenExpired = "Token hết hạn – mở Claude Code 1 lần để làm mới"
	noteNeedLogin    = "Cần đăng nhập lại Claude Code"
)

var (
	errTokenExpired  = errors.New("token đã hết hạn")
	errNoCredentials = errors.New("không có credentials hợp lệ")
)

// httpStatusError chỉ mang mã trạng thái, không bao giờ chứa header hay body.
type httpStatusError struct{ Code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("usage API trả HTTP %d", e.Code) }

// UsageWindow là 1 cửa sổ giới hạn (5 giờ hoặc 7 ngày).
type UsageWindow struct {
	Percent  float64   `json:"utilization"`
	ResetsAt time.Time `json:"resets_at"` // zero = server không trả hoặc sai định dạng
}

// ServerUsage là phần usage lấy từ server. Cửa sổ nil = server không trả (hoặc sai kiểu).
// Struct này cũng là định dạng file cache, tuyệt đối không có token.
type ServerUsage struct {
	FiveHour  *UsageWindow `json:"five_hour,omitempty"`
	SevenDay  *UsageWindow `json:"seven_day,omitempty"`
	FetchedAt time.Time    `json:"fetched_at"`
}

// ParseServerUsage đọc response; field thiếu hoặc sai kiểu chỉ làm cửa sổ/field đó bị bỏ,
// không làm hỏng phần còn lại. Lỗi chỉ khi không đọc được gì hữu ích.
func ParseServerUsage(data []byte) (ServerUsage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return ServerUsage{}, errors.New("response không phải JSON object")
	}
	u := ServerUsage{FiveHour: parseWindow(top["five_hour"]), SevenDay: parseWindow(top["seven_day"])}
	if u.FiveHour == nil && u.SevenDay == nil {
		return ServerUsage{}, errors.New("response không có five_hour/seven_day hợp lệ")
	}
	return u, nil
}

func parseWindow(raw json.RawMessage) *UsageWindow {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var pct *float64 // con trỏ để phân biệt null với 0
	if json.Unmarshal(m["utilization"], &pct) != nil || pct == nil || *pct < 0 {
		return nil
	}
	w := &UsageWindow{Percent: *pct}
	var s string
	if json.Unmarshal(m["resets_at"], &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			w.ResetsAt = t
		}
	}
	return w
}

// credentialsFile chỉ khai báo 2 field cần dùng; phần mcpOAuth và refreshToken không bao giờ được giải mã.
type credentialsFile struct {
	ClaudeAiOauth *struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"` // epoch mili-giây
	} `json:"claudeAiOauth"`
}

// credentialsPath là %USERPROFILE%\.claude\.credentials.json.
func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("không xác định được thư mục home: %w", err)
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

// readAccessToken chỉ đọc file, không bao giờ ghi hay refresh. Lỗi trả về không chứa nội dung file.
func readAccessToken(path string, now time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoCredentials
	}
	if err != nil {
		return "", fmt.Errorf("không đọc được credentials: %w", err)
	}
	var c credentialsFile
	if json.Unmarshal(data, &c) != nil {
		return "", errors.New("credentials.json sai định dạng")
	}
	o := c.ClaudeAiOauth
	if o == nil || o.AccessToken == "" {
		return "", errNoCredentials
	}
	if o.ExpiresAt <= 0 {
		return "", errors.New("credentials.json thiếu expiresAt")
	}
	if !now.Before(time.UnixMilli(o.ExpiresAt)) {
		return "", errTokenExpired
	}
	return o.AccessToken, nil
}

// fetchServerUsage gọi API 1 lần. Không theo redirect để token không bị gửi sang nơi khác.
func fetchServerUsage(ctx context.Context, client *http.Client, url, token string) (ServerUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, usageAPITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ServerUsage{}, errors.New("không tạo được request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", usageAPIBeta)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return ServerUsage{}, fmt.Errorf("gọi usage API: %w", err) // url.Error chỉ chứa method + URL
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ServerUsage{}, &httpStatusError{Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsageBodyBytes))
	if err != nil {
		return ServerUsage{}, fmt.Errorf("đọc response usage: %w", err)
	}
	return ParseServerUsage(body)
}

// UsageSnapshot là thứ menu hiển thị.
type UsageSnapshot struct {
	Usage ServerUsage
	Stale bool   // số đang hiển thị là số cũ (lần gọi gần nhất thất bại hoặc mới đọc từ cache)
	Note  string // thông báo lỗi cho người dùng, rỗng nếu không có
}

// UsageClient gọi API theo đúng các luật: tối thiểu 60 giây giữa 2 lần, 429 thì nghỉ 30 phút.
type UsageClient struct {
	credPath, cachePath, url string
	http                     *http.Client
	now                      func() time.Time

	mu           sync.Mutex
	snap         UsageSnapshot
	lastAttempt  time.Time
	blockedUntil time.Time
}

// NewUsageClient nạp cache (nếu có) để menu có số ngay khi khởi động.
func NewUsageClient(credPath, cachePath string) *UsageClient {
	c := &UsageClient{
		credPath: credPath, cachePath: cachePath, url: usageAPIURL, now: time.Now,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if data, err := os.ReadFile(cachePath); err == nil {
		var u ServerUsage
		if err := json.Unmarshal(data, &u); err == nil && !u.FetchedAt.IsZero() {
			c.snap = UsageSnapshot{Usage: u, Stale: true}
		} else {
			moduleLog("usage-api").Warn("cache usage hỏng, bỏ qua", "path", cachePath)
		}
	}
	return c
}

// NewDefaultUsageClient dùng credentials của Claude Code và cache trong appDataDir.
func NewDefaultUsageClient(appDir string) (*UsageClient, error) {
	cred, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	return NewUsageClient(cred, filepath.Join(appDir, "usage.json")), nil
}

// Snapshot trả về dữ liệu hiện có, không gọi mạng.
func (c *UsageClient) Snapshot() UsageSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

// Refresh gọi API nếu luật cho phép; ngược lại trả về snapshot hiện có. Token chỉ nằm trong biến cục bộ.
func (c *UsageClient) Refresh(ctx context.Context) UsageSnapshot {
	log := moduleLog("usage-api")
	c.mu.Lock()
	now := c.now()
	if now.Before(c.blockedUntil) || now.Sub(c.lastAttempt) < usageMinGap {
		log.Info("bỏ qua lần gọi (đang nghỉ hoặc gọi quá sát)", "blocked_until", c.blockedUntil.Format(time.RFC3339))
		s := c.snap
		c.mu.Unlock()
		return s
	}
	token, err := readAccessToken(c.credPath, now)
	if err != nil {
		c.recordFailure(err, now)
		s := c.snap
		c.mu.Unlock()
		log.Warn("không gọi API vì credentials", "err", err)
		return s
	}
	c.lastAttempt = now // đặt trước khi nhả khoá để lần gọi song song bị chặn bởi usageMinGap
	c.mu.Unlock()

	log.Info("bắt đầu lấy usage từ server")
	usage, err := fetchServerUsage(ctx, c.http, c.url, token)
	token = ""

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.recordFailure(err, now)
		log.Warn("lấy usage thất bại, giữ số cũ", "err", err)
		return c.snap
	}
	usage.FetchedAt = now
	c.snap = UsageSnapshot{Usage: usage}
	c.blockedUntil = time.Time{}
	if err := c.saveCache(usage); err != nil {
		log.Warn("không ghi được cache usage", "err", err)
	}
	log.Info("đã lấy usage")
	return c.snap
}

// recordFailure phải được gọi khi đang giữ c.mu.
func (c *UsageClient) recordFailure(err error, now time.Time) {
	var he *httpStatusError
	note := ""
	switch {
	case errors.Is(err, errTokenExpired):
		note = noteTokenExpired
	case errors.Is(err, errNoCredentials):
		note = noteNeedLogin
	case errors.As(err, &he) && (he.Code == http.StatusUnauthorized || he.Code == http.StatusForbidden):
		note = noteNeedLogin
	case errors.As(err, &he) && he.Code == http.StatusTooManyRequests:
		c.blockedUntil = now.Add(usageBackoff429)
		note = "Bị giới hạn tần suất, thử lại lúc " + c.blockedUntil.Format("15:04")
	case c.snap.Usage.FetchedAt.IsZero():
		note = "Không lấy được usage (xem log)"
	}
	c.snap.Note = note
	c.snap.Stale = !c.snap.Usage.FetchedAt.IsZero()
}

// saveCache ghi file tạm rồi rename để không để lại file nửa vời.
func (c *UsageClient) saveCache(u ServerUsage) error {
	data, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	tmp := c.cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("ghi %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.cachePath); err != nil {
		return fmt.Errorf("rename cache: %w", err)
	}
	return nil
}

// FiveHourReset là giờ reset phiên 5h do server báo (zero nếu chưa biết).
// Giờ reset của một phiên cố định nên số lấy từ lần gọi trước vẫn dùng được.
func (c *UsageClient) FiveHourReset() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.snap.Usage.FiveHour; w != nil {
		return w.ResetsAt
	}
	return time.Time{}
}

// FiveHourLine: "5h: 34% · reset 15:04".
func (s UsageSnapshot) FiveHourLine() string { return s.windowLine("5h", s.Usage.FiveHour, false) }

// WeekLine: "Tuần: 12% · reset thứ 5 04:00".
func (s UsageSnapshot) WeekLine() string { return s.windowLine("Tuần", s.Usage.SevenDay, true) }

func (s UsageSnapshot) windowLine(label string, w *UsageWindow, withWeekday bool) string {
	if w == nil {
		return label + ": --"
	}
	line := fmt.Sprintf("%s: %.0f%%", label, w.Percent)
	if !w.ResetsAt.IsZero() {
		r := w.ResetsAt.Local()
		if withWeekday {
			line += " · reset " + viWeekday(r.Weekday()) + " " + r.Format("15:04")
		} else {
			line += " · reset " + r.Format("15:04")
		}
	}
	if s.Stale && !s.Usage.FetchedAt.IsZero() {
		line += " (cũ, lúc " + s.Usage.FetchedAt.Local().Format("15:04") + ")"
	}
	return line
}

// FiveHourPercent cho biết % phiên 5h; ok=false nếu chưa có số.
func (s UsageSnapshot) FiveHourPercent() (float64, bool) {
	if s.Usage.FiveHour == nil {
		return 0, false
	}
	return s.Usage.FiveHour.Percent, true
}

func viWeekday(d time.Weekday) string {
	if d == time.Sunday {
		return "CN"
	}
	return fmt.Sprintf("thứ %d", int(d)+1)
}
