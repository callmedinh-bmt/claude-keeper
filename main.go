package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

// version được Makefile gán qua -ldflags "-X main.version=...".
var version = "dev"

const usageURL = "https://claude.ai/settings/usage"

//go:embed icon.ico
var iconData []byte

func main() {
	pingOnce := flag.Bool("ping", false, "gửi 1 tin rồi thoát (để test, không mở khay)")
	showUsage := flag.Bool("usage", false, "in usage phiên hiện tại rồi thoát")
	flag.Parse()

	closer, err := initLogger()
	if err != nil {
		fmt.Fprintln(os.Stderr, "không khởi tạo được log:", err)
		os.Exit(1)
	}
	defer closer.Close()
	log := moduleLog("main")
	log.Info("claude-keeper khởi động", "version", version)

	dir, err := appDataDir()
	if err != nil {
		log.Error("không có thư mục dữ liệu", "err", err)
		os.Exit(1)
	}
	store := NewStateStore(dir + `\state.json`)

	switch {
	case *pingOnce:
		runPingOnce(store)
		return
	case *showUsage:
		printUsage()
		return
	}

	// Chỉ cho 1 bản chạy, tránh 2 bản cùng gửi tin.
	if !acquireSingleInstance() {
		log.Warn("đã có bản khác đang chạy, thoát")
		return
	}
	app := &trayApp{store: store}
	systray.Run(app.onReady, app.onExit)
	log.Info("claude-keeper đã thoát")
}

// acquireSingleInstance dùng named mutex; mutex tự giải phóng khi process thoát.
func acquireSingleInstance() bool {
	name, err := windows.UTF16PtrFromString("Local\\claude-keeper-single-instance")
	if err != nil {
		return true
	}
	_, err = windows.CreateMutex(nil, false, name)
	return err != windows.ERROR_ALREADY_EXISTS
}

func runPingOnce(store *StateStore) {
	sched := NewScheduler(store, Ping, nil)
	if err := sched.PingNow(context.Background()); err != nil {
		moduleLog("main").Error("gửi thất bại", "err", err)
		os.Exit(1)
	}
}

func printUsage() {
	root, err := claudeProjectsDir()
	if err == nil {
		var u SessionUsage
		if u, err = ReadUsage(root, nowFunc()); err == nil {
			if !u.Active {
				fmt.Println("Chưa có phiên nào đang mở")
				return
			}
			fmt.Printf("tokens=%d (%s) start=%s end=%s\n", u.Tokens, formatTokens(u.Tokens),
				u.Start.Format(time.RFC3339), u.End.Format(time.RFC3339))
			return
		}
	}
	moduleLog("main").Error("không đọc được usage", "err", err)
	os.Exit(1)
}

// openWithShell mở URL/file bằng ứng dụng mặc định, không bật cửa sổ console.
func openWithShell(target string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Start()
}
