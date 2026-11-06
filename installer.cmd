@echo off
rem Installe ou met a jour le poste 3 MULCV (scanner reseau) dans C:\MULCV. Double-cliquer.
cd /d "%~dp0"
echo.
echo  Installation du poste 3 MULCV : scanner reseau HP LaserJet MFP M236dw (C:\MULCV)
echo  -------------------------------------------------------------------------------
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1"
pause
