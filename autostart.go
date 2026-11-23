package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "claude-keeper"
)

// AutostartEnabled đọc trạng thái trực tiếp từ registry (không cache) nên luôn đúng.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		moduleLog("autostart").Error("không mở được khoá Run", "err", err)
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		moduleLog("autostart").Error("không đọc được giá trị Run", "err", err)
	}
	return err == nil
}

// SetAutostart bật/tắt tự chạy khi đăng nhập Windows.
func SetAutostart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("mở khoá Run: %w", err)
	}
	defer k.Close()

	if !enable {
		if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("xoá giá trị Run: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("không xác định được đường dẫn exe: %w", err)
	}
	// Đặt trong dấu nháy để đường dẫn có khoảng trắng vẫn chạy được.
	if err := k.SetStringValue(runValueName, `"`+exe+`"`); err != nil {
		return fmt.Errorf("ghi giá trị Run: %w", err)
	}
	return nil
}
