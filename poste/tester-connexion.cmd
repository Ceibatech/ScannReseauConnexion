@echo off
rem Verifie la configuration, la connexion a MULCV et au scanner reseau. Double-cliquer.
chcp 65001 >nul
cd /d "%~dp0"
bin\mulcv-scan.exe -test
if errorlevel 1 (echo. & echo Echec : voir le message ci-dessus.) else (echo. & echo Tout est pret.)
pause
