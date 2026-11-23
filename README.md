# claude-keeper

App Windows chạy ở khay hệ thống (system tray):

1. Tự gửi 1 tin ngắn (`claude -p "hi" --model haiku`) ngay khi phiên 5 giờ của Claude hết, để phiên mới luôn được mở.
2. Hiện usage (token) của phiên 5 giờ hiện tại, đọc từ `%USERPROFILE%\.claude\projects\**\*.jsonl`.
3. Hiện % usage phiên 5h và tuần lấy từ server (xem mục dưới); icon khay đổi màu theo % phiên 5h: xanh < 50%, vàng 50–80%, đỏ > 80%.

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

## % usage từ server

Gọi `GET https://api.anthropic.com/api/oauth/usage` bằng access token trong `%USERPROFILE%\.claude\.credentials.json`. Đây là API nội bộ, không có tài liệu, có thể hỏng bất cứ lúc nào.

- App chỉ đọc token, không refresh, không ghi vào file credentials, không lưu hay log token. Token hết hạn thì menu báo "mở Claude Code 1 lần để làm mới".
- Tự gọi mỗi 10 phút và khi bấm "Làm mới usage"; 2 lần gọi cách nhau tối thiểu 60 giây. 429 thì nghỉ 30 phút; lỗi khác giữ số cũ kèm "(cũ, lúc HH:MM)".
- Cache: `%APPDATA%\claude-keeper\usage.json` (không chứa token).

## Dữ liệu và log

- Log: `%APPDATA%\claude-keeper\app.log` (hoặc menu "Xem log").
- Giờ gửi gần nhất: `%APPDATA%\claude-keeper\state.json`. `make uninstall` không xoá thư mục này.
