@echo off
setlocal DisableDelayedExpansion
if not exist "%~dp0bin\t3-bridge.exe" (
    echo Extract the entire ZIP and keep this launcher next to the bin folder.
    pause
    exit /b 1
)
"%~dp0bin\t3-bridge.exe" config gui
if not errorlevel 1 exit /b 0
echo.
echo To open the web workbench, run bin\t3-bridge.exe config web from this folder.
pause
exit /b 1
