@echo off
rem Builds release binaries into the releases folder.
setlocal
cd /d "%~dp0"
if not exist releases mkdir releases
set CGO_ENABLED=0
call :build windows 386   viewer-win32.exe   || exit /b 1
call :build linux   386   viewer-linux-386   || exit /b 1
call :build linux   amd64 viewer-linux-amd64 || exit /b 1
call :build linux   arm64 viewer-linux-arm64 || exit /b 1
echo Built releases\
exit /b 0

:build
set GOOS=%1
set GOARCH=%2
go build -trimpath -ldflags "-s -w" -o releases\%3 . || exit /b 1
echo   %1/%2 -^> releases\%3
exit /b 0
