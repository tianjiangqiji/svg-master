@echo off
rem ============================================================
rem  SVG Master - portable (green) build script
rem  Output: dist\svg-master\  (svg-master.exe + data\ + docs)
rem  NOTE: keep this file pure ASCII. cmd.exe parses .bat with the
rem  OEM codepage, so UTF-8 Chinese text here would corrupt parsing.
rem ============================================================
setlocal
cd /d "%~dp0"

set APPNAME=svg-master
set OUTDIR=dist\%APPNAME%
set LDFLAGS=-s -w -H windowsgui
set CGO_ENABLED=0

echo.
echo [1/5] Cleaning previous output ...
rem NOTE: keep this line ASCII - delete only exe and docs, KEEP data\ (user config + session SVGs)
if exist "%OUTDIR%\svg-master.exe" del /q "%OUTDIR%\svg-master.exe" 2>nul
if exist "%OUTDIR%\svg-master.exe~" del /q "%OUTDIR%\svg-master.exe~" 2>nul
if exist "%OUTDIR%\README.md" del /q "%OUTDIR%\README.md" 2>nul
if exist "%OUTDIR%\README-FIRST.txt" del /q "%OUTDIR%\README-FIRST.txt" 2>nul
if not exist "%OUTDIR%" mkdir "%OUTDIR%" 2>nul
if not exist "%OUTDIR%\data" mkdir "%OUTDIR%\data" 2>nul
if errorlevel 1 goto :fail

echo [2/5] Resolving modules ...
set GOFLAGS=-mod=mod
go mod tidy
if errorlevel 1 echo   [warn] go mod tidy failed, continuing

echo [3/5] Building %APPNAME%.exe ...
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\%APPNAME%.exe" .
if errorlevel 1 goto :fail

echo [4/5] Copying extra files ...
if exist "README.md" copy /y "README.md" "%OUTDIR%\README.md" >nul 2>&1
if exist "README-FIRST.txt" copy /y "README-FIRST.txt" "%OUTDIR%\README-FIRST.txt" >nul 2>&1

echo [5/5] Verifying ...
if not exist "%OUTDIR%\%APPNAME%.exe" goto :fail
for %%F in ("%OUTDIR%\%APPNAME%.exe") do set SIZE=%%~zF

echo.
echo ============================================================
echo  Build finished
echo  Folder: %OUTDIR%
echo  exe   : %APPNAME%.exe  (%SIZE% bytes)
echo ============================================================
dir /b "%OUTDIR%"
echo.
exit /b 0

:fail
echo.
echo [ERROR] Build failed.
exit /b 1
