@echo off
rem Numerise sur le scanner reseau et depose chaque lot dans MULCV. Double-cliquer.
title Numerisation MULCV - poste 3
chcp 65001 >nul
cd /d "%~dp0"
bin\mulcv-scan.exe -scan
pause
