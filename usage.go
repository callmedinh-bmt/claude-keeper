package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// usageLookback: chỉ đọc file/dòng trong 24h gần nhất để mỗi phút quét không quá nặng,
// vẫn đủ dài để dựng chuỗi các phiên 5h dẫn tới phiên hiện tại.
const usageLookback = 24 * time.Hour

// SessionUsage là kết quả cho phiên 5 giờ hiện tại.
type SessionUsage struct {
	Active bool      // có phiên đang mở không
	Tokens int64     // input + output + cache creation + cache read
	Start  time.Time // đầu phiên (làm tròn xuống giờ)
	End    time.Time // Start + 5h
}

// usageEntry là 1 dòng "assistant" có usage trong file JSONL.
type usageEntry struct {
	At     time.Time
	Tokens int64
	key    string // message.id + requestId, để loại dòng trùng
}

// jsonlLine chỉ khai báo các field có thật trong file ~/.claude/projects/**/*.jsonl
// (đã kiểm tra trên file thật): type, timestamp, requestId, message.id, message.usage.*
type jsonlLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	Message   *struct {
		ID    string `json:"id"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var assistantMarker = []byte(`"type":"assistant"`)

// parseLine trả về entry nếu dòng hợp lệ; ok=false thì bỏ qua (dòng lỗi/thiếu field/không phải assistant).
func parseLine(line []byte) (usageEntry, bool) {
	// Lọc rẻ trước khi Unmarshal: file có nhiều dòng blob rất lớn không cần parse.
	if !bytes.Contains(line, assistantMarker) {
		return usageEntry{}, false
	}
	var l jsonlLine
	if err := json.Unmarshal(line, &l); err != nil {
		return usageEntry{}, false
	}
	if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil {
		return usageEntry{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		return usageEntry{}, false
	}
	u := l.Message.Usage
	key := ""
	if l.Message.ID != "" || l.RequestID != "" {
		key = l.Message.ID + "|" + l.RequestID
	}
	return usageEntry{At: at, Tokens: u.Input + u.Output + u.CacheCreation + u.CacheRead, key: key}, true
}

// readEntries đọc mọi *.jsonl dưới root (cả thư mục con, kể cả subagents), đã loại dòng trùng.
func readEntries(root string, since time.Time) ([]usageEntry, error) {
	log := moduleLog("usage")
	var entries []usageEntry
	seen := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Warn("bỏ qua đường dẫn không đọc được", "path", path, "err", err)
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(since) {
			return nil // file không được ghi từ trước mốc → chắc chắn không có dòng mới hơn
		}
		f, err := os.Open(path)
		if err != nil {
			log.Warn("không mở được file", "path", path, "err", err)
			return nil
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 128<<20) // dòng blob có thể rất lớn
		for sc.Scan() {
			e, ok := parseLine(sc.Bytes())
			if !ok || e.At.Before(since) {
				continue
			}
			if e.key != "" {
				if seen[e.key] {
					continue // cùng 1 message được ghi nhiều dòng khi streaming
				}
				seen[e.key] = true
			}
			entries = append(entries, e)
		}
		if err := sc.Err(); err != nil {
			log.Warn("đọc file dở dang", "path", path, "err", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("duyệt %s: %w", root, err)
	}
	return entries, nil
}

// SumSession dựng các phiên 5h từ các entry: phiên mở ở entry đầu tiên (làm tròn xuống giờ)
// và kéo dài 5h; entry nằm ngoài thì mở phiên mới. Trả về phiên chứa now (nếu có).
func SumSession(entries []usageEntry, now time.Time) SessionUsage {
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })

	var cur SessionUsage
	for _, e := range entries {
		if e.At.After(now) {
			continue
		}
		if cur.End.IsZero() || !e.At.Before(cur.End) {
			start := e.At.UTC().Truncate(time.Hour)
			cur = SessionUsage{Start: start.Local(), End: start.Add(sessionLength).Local()}
		}
		cur.Tokens += e.Tokens
	}
	cur.Active = !cur.End.IsZero() && now.Before(cur.End)
	if !cur.Active {
		return SessionUsage{}
	}
	return cur
}

// ReadUsage đọc root (thường là ~/.claude/projects) và tính usage của phiên hiện tại.
func ReadUsage(root string, now time.Time) (SessionUsage, error) {
	entries, err := readEntries(root, now.Add(-usageLookback))
	if err != nil {
		return SessionUsage{}, err
	}
	return SumSession(entries, now), nil
}

// claudeProjectsDir là %USERPROFILE%\.claude\projects.
func claudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("không xác định được thư mục home: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// formatTokens rút gọn số token: 950, 12.3K, 4.56M.
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}
