package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	pingTimeout     = 3 * time.Minute
	createNoWindow  = 0x08000000 // CREATE_NO_WINDOW: không bật cửa sổ console
	maxLoggedOutput = 500
)

// Ping chạy `claude -p "hi" --model haiku` để mở phiên 5 giờ mới.
func Ping(ctx context.Context) error {
	log := moduleLog("pinger")

	// LookPath tự thử các đuôi trong PATHEXT nên tìm được cả claude.exe lẫn claude.cmd (cài qua npm).
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("không tìm thấy lệnh claude trong PATH: %w", err)
	}

	// Chạy trong thư mục trống cố định để Claude Code không nạp CLAUDE.md/project nào,
	// và mọi lần ping dùng chung 1 thư mục project trong ~/.claude/projects.
	workDir := filepath.Join(os.TempDir(), "claude-keeper")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("không tạo được thư mục tạm %s: %w", workDir, err)
	}

	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath, "-p", "hi", "--model", "haiku")
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}

	log.Info("bắt đầu gửi tin", "claude", claudePath)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if len(output) > maxLoggedOutput {
		output = output[:maxLoggedOutput] + "..."
	}
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("claude quá thời gian %s: %w (output: %q)", pingTimeout, ctx.Err(), output)
		}
		return fmt.Errorf("claude lỗi: %w (output: %q)", err, output)
	}
	log.Info("gửi tin thành công", "took", time.Since(start).Round(time.Millisecond).String(), "reply", output)
	return nil
}
