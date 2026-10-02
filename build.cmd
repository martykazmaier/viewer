@echo off
rem Builds viewer.exe (32-bit, runs on any Windows) and viewer64.exe.
setlocal
cd /d "%~dp0"
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=386
go build -trimpath -ldflags "-s -w" -o viewer.exe . || exit /b 1
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o viewer64.exe . || exit /b 1
echo Built viewer.exe and viewer64.exe
