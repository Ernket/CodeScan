@echo off
setlocal

set "ROOT_DIR=%~dp0"
set "OUT_DIR=%ROOT_DIR%releases"
set "FRONTEND_DIR=%ROOT_DIR%frontend"
set "WINDOWS_DIR=%OUT_DIR%\windows-amd64"
set "LINUX_DIR=%OUT_DIR%\linux-amd64"
set "WINDOWS_INIT=%WINDOWS_DIR%\init.exe"
set "WINDOWS_CODESCAN=%WINDOWS_DIR%\codescan.exe"
set "LINUX_INIT=%LINUX_DIR%\init"
set "LINUX_CODESCAN=%LINUX_DIR%\codescan"
set "LINUX_SETUP=%LINUX_DIR%\setup-linux.sh"
set "WINDOWS_ZIP=%OUT_DIR%\CodeScan-windows-amd64.zip"
set "LINUX_TAR=%OUT_DIR%\CodeScan-linux-amd64.tar.gz"

cd /d "%ROOT_DIR%" || exit /b 1

where go >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Go was not found in PATH.
  exit /b 1
)

where npm >nul 2>nul
if errorlevel 1 (
  echo [ERROR] npm was not found in PATH.
  exit /b 1
)

where tar >nul 2>nul
if errorlevel 1 (
  echo [ERROR] tar was not found in PATH.
  exit /b 1
)

if not exist "%OUT_DIR%" mkdir "%OUT_DIR%"
if not exist "%WINDOWS_DIR%" mkdir "%WINDOWS_DIR%"
if not exist "%LINUX_DIR%" mkdir "%LINUX_DIR%"

echo Cleaning previous release artifacts...
del /f /q "%OUT_DIR%\init.exe" "%OUT_DIR%\codescan.exe" "%WINDOWS_ZIP%" "%LINUX_TAR%" 2>nul
del /f /q "%WINDOWS_INIT%" "%WINDOWS_CODESCAN%" "%WINDOWS_DIR%\config.example.json" "%WINDOWS_DIR%\README.md" "%WINDOWS_DIR%\LICENSE" 2>nul
del /f /q "%LINUX_INIT%" "%LINUX_CODESCAN%" "%LINUX_DIR%\config.example.json" "%LINUX_DIR%\README.md" "%LINUX_DIR%\LICENSE" "%LINUX_SETUP%" 2>nul

echo [1/8] Preparing frontend dependencies...
if not exist "%FRONTEND_DIR%\node_modules" (
  call npm --prefix "%FRONTEND_DIR%" ci
  if errorlevel 1 goto :failed
)

echo [2/8] Building frontend assets...
call npm --prefix "%FRONTEND_DIR%" run build
if errorlevel 1 goto :failed

echo [3/8] Building Windows init.exe...
set "GOOS=windows"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -trimpath -ldflags="-s -w" -o "%WINDOWS_INIT%" .\cmd\init
if errorlevel 1 goto :failed

echo [4/8] Building Windows codescan.exe with embedded frontend...
go build -trimpath -tags embedded_frontend -ldflags="-s -w" -o "%WINDOWS_CODESCAN%" .
if errorlevel 1 goto :failed

echo [5/8] Building Linux init and codescan with embedded frontend...
set "GOOS=linux"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -trimpath -ldflags="-s -w" -o "%LINUX_INIT%" .\cmd\init
if errorlevel 1 goto :failed
go build -trimpath -tags embedded_frontend -ldflags="-s -w" -o "%LINUX_CODESCAN%" .
if errorlevel 1 goto :failed

echo [6/8] Preparing package files...
copy /Y "%ROOT_DIR%data\config.example.json" "%WINDOWS_DIR%\config.example.json" >nul
if errorlevel 1 goto :failed
copy /Y "%ROOT_DIR%README.md" "%WINDOWS_DIR%\README.md" >nul
if errorlevel 1 goto :failed
copy /Y "%ROOT_DIR%LICENSE" "%WINDOWS_DIR%\LICENSE" >nul
if errorlevel 1 goto :failed
copy /Y "%ROOT_DIR%data\config.example.json" "%LINUX_DIR%\config.example.json" >nul
if errorlevel 1 goto :failed
copy /Y "%ROOT_DIR%README.md" "%LINUX_DIR%\README.md" >nul
if errorlevel 1 goto :failed
copy /Y "%ROOT_DIR%LICENSE" "%LINUX_DIR%\LICENSE" >nul
if errorlevel 1 goto :failed
(
  echo #!/usr/bin/env sh
  echo set -eu
  echo chmod +x ./init ./codescan
  echo echo "Linux binaries are executable. Run ./init first, then ./codescan."
) > "%LINUX_SETUP%"

echo [7/8] Creating Windows zip package...
powershell -NoProfile -ExecutionPolicy Bypass -Command "Compress-Archive -Path '%WINDOWS_DIR%\*' -DestinationPath '%WINDOWS_ZIP%' -Force"
if errorlevel 1 goto :failed

echo [8/8] Creating Linux tar.gz package...
tar -czf "%LINUX_TAR%" -C "%LINUX_DIR%" .
if errorlevel 1 goto :failed

echo.
echo Build completed.
echo Output directory: %OUT_DIR%
dir "%OUT_DIR%"
echo.
echo Windows files:
dir "%WINDOWS_DIR%"
echo.
echo Linux files:
dir "%LINUX_DIR%"
exit /b 0

:failed
echo.
echo [ERROR] Build failed.
exit /b 1
