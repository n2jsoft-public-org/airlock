@echo off
rem Runs install.ps1 without changing the PowerShell execution policy.
rem Usage: install.cmd [-InstallDir <path>] [-Version <tag>] [-SkipPath] [-Uninstall [-Purge]]
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
set code=%errorlevel%
if not "%code%"=="0" pause
exit /b %code%
