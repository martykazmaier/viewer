@echo off
rem Builds release zip archives into the releases folder.
rem Each zip has the binary, README.md, LICENSE and file_id.diz.
setlocal EnableExtensions
cd /d "%~dp0"
if not exist releases mkdir releases
set CGO_ENABLED=0
set STAGE=%TEMP%\viewer-release-stage
if exist "%STAGE%" rmdir /s /q "%STAGE%"
mkdir "%STAGE%" || exit /b 1

call :pack windows 386   viewer-win32.exe   viewer-win32.zip       || goto :fail
call :pack linux   386   viewer-linux-386   viewer-linux-386.zip   || goto :fail
call :pack linux   amd64 viewer-linux-amd64 viewer-linux-amd64.zip || goto :fail
call :pack linux   arm64 viewer-linux-arm64 viewer-linux-arm64.zip || goto :fail
rmdir /s /q "%STAGE%"
echo Built releases\
exit /b 0

:fail
rmdir /s /q "%STAGE%" 2>nul
exit /b 1

:pack
set GOOS=%1
set GOARCH=%2
del /q "%STAGE%\*" 2>nul
go build -trimpath -ldflags "-s -w" -o "%STAGE%\%3" . || exit /b 1
copy /y README.md "%STAGE%\README.md" >nul
copy /y LICENSE "%STAGE%\LICENSE" >nul
copy /y file_id.diz "%STAGE%\FILE_ID.DIZ" >nul
tar -a -c -f "releases\%4" -C "%STAGE%" %3 README.md LICENSE FILE_ID.DIZ || exit /b 1
echo   %1/%2 -^> releases\%4
exit /b 0
