package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// appDataDir trả về %APPDATA%\claude-keeper (tự tạo nếu chưa có).
func appDataDir() (string, error) {
	base, err := os.UserConfigDir() // = %APPDATA% trên Windows
	if err != nil {
		return "", fmt.Errorf("không xác định được thư mục APPDATA: %w", err)
	}
	dir := filepath.Join(base, "claude-keeper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("không tạo được thư mục %s: %w", dir, err)
	}
	return dir, nil
}

type stateFile struct {
	LastPing string `json:"last_ping"`
}

// StateStore lưu giờ gửi gần nhất; an toàn khi nhiều goroutine cùng dùng.
type StateStore struct {
	mu       sync.Mutex
	path     string
	lastPing time.Time
}

// NewStateStore đọc state từ path. File chưa có hoặc hỏng thì coi như chưa gửi lần nào.
func NewStateStore(path string) *StateStore {
	s := &StateStore{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			moduleLog("state").Error("không đọc được state", "path", path, "err", err)
		}
		return s
	}
	var f stateFile
	if err := json.Unmarshal(data, &f); err != nil {
		moduleLog("state").Error("state.json hỏng, bỏ qua", "path", path, "err", err)
		return s
	}
	if t, err := time.Parse(time.RFC3339, f.LastPing); err == nil {
		s.lastPing = t
	} else {
		moduleLog("state").Error("last_ping sai định dạng", "value", f.LastPing, "err", err)
	}
	return s
}

// LastPing trả về giờ gửi gần nhất (zero nếu chưa có).
func (s *StateStore) LastPing() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPing
}

// SetLastPing cập nhật và ghi file. Ghi vào file tạm rồi rename để không bao giờ để lại file nửa vời.
func (s *StateStore) SetLastPing(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(stateFile{LastPing: t.Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("ghi %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	s.lastPing = t
	return nil
}
