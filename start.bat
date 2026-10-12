@echo off
setlocal DisableDelayedExpansion
"%~dp0artex.exe" %*
set "code=%ERRORLEVEL%"
if not "%code%"=="0" echo [VulnHarvest] Startup failed with exit code %code%. 1>&2
exit /b %code%
