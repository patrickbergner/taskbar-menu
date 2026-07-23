@echo off
setlocal
set ROOT=%~dp0
set BIN=%ROOT%bin

pushd "%ROOT%"

if not exist "%BIN%" mkdir "%BIN%"

set /p VERSION=<"%ROOT%VERSION"

rem The Go toolchain has no resource compiler, so the icon is stamped into the finished PE
rem using this little helper tool. It writes the target's resource directory as file data
rem rather than loading it, so one host-arch build of SetIcon can stamp exes of either
rem target architecture below.
echo [1/4] Build SetIcon
set GOOS=windows
go build -trimpath -ldflags="-s -w" -o "%BIN%\SetIcon.exe" ./src/seticon
if errorlevel 1 goto :fail

echo [2/4] Build TaskbarMenu (AMD64)
set GOARCH=amd64
go build -trimpath -ldflags="-s -w -H windowsgui" -o "%BIN%\TaskbarMenu-AMD64.exe" ./src
if errorlevel 1 goto :fail

echo [3/4] Build TaskbarMenu (ARM64)
set GOARCH=arm64
go build -trimpath -ldflags="-s -w -H windowsgui" -o "%BIN%\TaskbarMenu-ARM64.exe" ./src
if errorlevel 1 goto :fail
set GOARCH=

echo [4/4] Set Application Icon and Version (%VERSION%)
 "%BIN%\SetIcon.exe" "%BIN%\TaskbarMenu-AMD64.exe" "%ROOT%app-icon\icon.ico" "%VERSION%"
if errorlevel 1 goto :fail
"%BIN%\SetIcon.exe" "%BIN%\TaskbarMenu-ARM64.exe" "%ROOT%app-icon\icon.ico" "%VERSION%"
if errorlevel 1 goto :fail

echo.
echo Done: %BIN%\TaskbarMenu-AMD64.exe, %BIN%\TaskbarMenu-ARM64.exe
popd
goto :eof

:fail
echo.
echo BUILD FAILED
popd
exit /b 1
