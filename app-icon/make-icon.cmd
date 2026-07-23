@echo off
setlocal enabledelayedexpansion

rem Rasterizes icon.svg into icon.ico.
rem Requires ImageMagick (https://imagemagick.org) with SVG support on PATH.

set ROOT=%~dp0
set SVG=%ROOT%icon.svg
set OUT=%ROOT%icon.ico
set SIZES=16 20 24 32 40 48 64 256

set MAGICK=magick
where magick >nul 2>nul
if errorlevel 1 (
    echo make-icon: ImageMagick 'magick' not found on PATH.
    exit /b 1
)

set TMP=%TEMP%\taskbarmenu-make-icon
if exist "%TMP%" rmdir /s /q "%TMP%"
mkdir "%TMP%"

set PNGS=
for %%S in (%SIZES%) do (
    "!MAGICK!" -background none "%SVG%[%%Sx%%S]" "%TMP%\%%S.png"
    if errorlevel 1 goto :fail
    set PNGS=!PNGS! "%TMP%\%%S.png"
)

"!MAGICK!" !PNGS! "%OUT%"
if errorlevel 1 goto :fail

rmdir /s /q "%TMP%"
echo Wrote %OUT%
goto :eof

:fail
echo make-icon FAILED
rmdir /s /q "%TMP%" 2>nul
exit /b 1
