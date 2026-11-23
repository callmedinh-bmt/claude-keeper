# Chạy bằng GNU Make trên Windows, shell là PowerShell.
SHELL := powershell.exe
.SHELLFLAGS := -NoProfile -Command

APP_NAME := claude-keeper
VERSION := $(or $(shell git describe --tags --always --dirty 2>$$null),dev)
BIN_DIR := bin
EXE := $(BIN_DIR)/$(APP_NAME).exe
INSTALL_DIR := $$env:LOCALAPPDATA\$(APP_NAME)

.PHONY: help run build build-dev test vet fmt check install uninstall clean
.DEFAULT_GOAL := help

help: ## In danh sách target
	@[Console]::OutputEncoding = [Text.Encoding]::UTF8; Select-String -Path Makefile -Pattern '^([a-z-]+):.*## (.*)$$' | ForEach-Object { '{0,-10} {1}' -f $$_.Matches[0].Groups[1].Value, $$_.Matches[0].Groups[2].Value }

run: ## Chạy khi dev (có console, log hiện ra màn hình)
	go run .

build: ## Build bản release (windowsgui, không có cửa sổ console)
	go build -ldflags '-H=windowsgui -s -w -X main.version=$(VERSION)' -o $(EXE)

build-dev: ## Build bản có console để debug
	go build -ldflags '-X main.version=$(VERSION)' -o $(EXE)

test: ## Chạy unit test
	go test ./... -v

vet: ## go vet
	go vet ./...

fmt: ## gofmt toàn bộ code
	gofmt -w .

check: fmt vet test ## Chạy fmt, vet, test theo thứ tự

install: build ## Build rồi copy exe vào %LOCALAPPDATA%\claude-keeper
	Stop-Process -Name $(APP_NAME) -ErrorAction SilentlyContinue; Start-Sleep -Milliseconds 500
	New-Item -ItemType Directory -Force "$(INSTALL_DIR)" | Out-Null
	Copy-Item $(EXE) "$(INSTALL_DIR)\" -Force
	@Write-Host "Installed to $(INSTALL_DIR)"

uninstall: ## Tắt app, xoá khoá tự chạy và thư mục cài
	Stop-Process -Name $(APP_NAME) -ErrorAction SilentlyContinue; Start-Sleep -Milliseconds 500
	Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name $(APP_NAME) -ErrorAction SilentlyContinue; exit 0
	Remove-Item -Recurse -Force "$(INSTALL_DIR)" -ErrorAction SilentlyContinue; exit 0

clean: ## Xoá thư mục bin
	Remove-Item -Recurse -Force $(BIN_DIR) -ErrorAction SilentlyContinue; exit 0
