@echo off
setlocal

set "ROOT=%~dp0"
set "IBM_DB_HOME=%ROOT%clidriver"
set "PATH=%IBM_DB_HOME%\bin;%PATH%"

if not exist "%ROOT%sync_diff_inspector.exe" (
  echo [ERROR] Missing executable: %ROOT%sync_diff_inspector.exe
  exit /b 1
)

if not exist "%IBM_DB_HOME%\bin\db2cli64.dll" (
  echo [ERROR] Incomplete IBM CLI driver: %IBM_DB_HOME%
  exit /b 1
)

set "CONFIG=%~1"
if "%CONFIG%"=="" set "CONFIG=%ROOT%config.toml"

if not exist "%CONFIG%" (
  echo [ERROR] Missing configuration: %CONFIG%
  echo Copy config.example.toml to config.toml and fill in the connection settings.
  exit /b 1
)

"%ROOT%sync_diff_inspector.exe" -C "%CONFIG%"
exit /b %ERRORLEVEL%
