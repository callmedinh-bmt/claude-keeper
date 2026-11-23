# claude-keeper

App Windows chạy ở khay hệ thống (system tray):

1. Tự gửi 1 tin ngắn (`claude -p "hi" --model haiku`) ngay khi phiên 5 giờ của Claude hết, để phiên mới luôn được mở.
2. Hiện usage (token) của phiên 5 giờ hiện tại, đọc từ `%USERPROFILE%\.claude\projects\**\*.jsonl`.

## Cần cài

- [Go](https://go.dev/dl/) bản mới nhất.
- [Claude Code](https://docs.claude.com/en/docs/claude-code) đã đăng nhập bằng gói Pro/Max, lệnh `claude` nằm trong PATH.
- GNU Make: `winget install GnuWin32.Make` hoặc `choco install make`.

## Dùng

```powershell
make          # xem danh sách target
make run      # chạy khi dev (log hiện ra console)
make check    # fmt + vet + test
make build    # bin\claude-keeper.exe (windowsgui, không có cửa sổ đen)
make install  # build + copy vào %LOCALAPPDATA%\claude-keeper\
make uninstall
make clean
```

Sau `make install`, chạy `%LOCALAPPDATA%\claude-keeper\claude-keeper.exe`, bấm chuột phải vào icon ở khay và tick "Tự chạy khi bật máy".

Cờ dòng lệnh để test không cần khay: `claude-keeper.exe -ping` (gửi 1 tin), `claude-keeper.exe -usage` (in usage).

## Dữ liệu và log

- Log: `%APPDATA%\claude-keeper\app.log` (hoặc menu "Xem log").
- Giờ gửi gần nhất: `%APPDATA%\claude-keeper\state.json`. `make uninstall` không xoá thư mục này.
