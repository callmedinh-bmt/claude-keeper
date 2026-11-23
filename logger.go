package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const maxLogBytes = 1 << 20 // quá 1MB thì xoay file

// logFilePath là đường dẫn app.log trong %APPDATA%\claude-keeper.
func logFilePath() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "app.log"), nil
}

// initLogger ghi log (timestamp ISO 8601, level, module, message) ra app.log.
// Khi chạy có console (make run) log cũng hiện ra stderr; bản windowsgui không có console nên ghi stderr sẽ lỗi và bị bỏ qua.
func initLogger() (io.Closer, error) {
	path, err := logFilePath()
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".old") // giữ 1 bản cũ; lỗi rename không quan trọng
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mở log %s: %w", path, err)
	}
	// File đứng trước: nếu stderr không hợp lệ thì dòng log vẫn đã vào file.
	handler := slog.NewTextHandler(io.MultiWriter(f, os.Stderr), &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	return f, nil
}

// moduleLog trả về logger gắn nhãn module.
func moduleLog(module string) *slog.Logger {
	return slog.Default().With("module", module)
}
