@echo off
setlocal EnableExtensions EnableDelayedExpansion

set "ROOT_DIR=%~dp0.."
set "BIN_DIR=%ROOT_DIR%\bin"
set "BIN=%BIN_DIR%\gotunnel-client.exe"

if "%GOTUNNEL_SERVER_ADDR%"=="" (set "SERVER_ADDR=127.0.0.1:6000") else (set "SERVER_ADDR=%GOTUNNEL_SERVER_ADDR%")
if "%GOTUNNEL_LOCAL_ADDR%"=="" (set "LOCAL_ADDR=127.0.0.1:5900") else (set "LOCAL_ADDR=%GOTUNNEL_LOCAL_ADDR%")
if "%GOTUNNEL_TLS%"=="" (set "USE_TLS=false") else (set "USE_TLS=%GOTUNNEL_TLS%")
if "%GOTUNNEL_DEBUG%"=="" (set "DEBUG=false") else (set "DEBUG=%GOTUNNEL_DEBUG%")
set "BUILD_ONLY=false"
set "GO_RUN=false"

:parse_args
if "%~1"=="" goto after_parse
if /I "%~1"=="-h" goto help
if /I "%~1"=="--help" goto help
if /I "%~1"=="-s" goto arg_server
if /I "%~1"=="--server" goto arg_server
if /I "%~1"=="-l" goto arg_local
if /I "%~1"=="--local" goto arg_local
if /I "%~1"=="--tls" (
  set "USE_TLS=true"
  shift
  goto parse_args
)
if /I "%~1"=="--debug" (
  set "DEBUG=true"
  shift
  goto parse_args
)
if /I "%~1"=="--build-only" (
  set "BUILD_ONLY=true"
  shift
  goto parse_args
)
if /I "%~1"=="--go-run" (
  set "GO_RUN=true"
  shift
  goto parse_args
)
echo Unknown option: %~1 1>&2
goto help_error

:arg_server
if "%~2"=="" (
  echo Missing value for %~1 1>&2
  goto help_error
)
set "SERVER_ADDR=%~2"
shift
shift
goto parse_args

:arg_local
if "%~2"=="" (
  echo Missing value for %~1 1>&2
  goto help_error
)
set "LOCAL_ADDR=%~2"
shift
shift
goto parse_args

:after_parse
if not exist "%ROOT_DIR%\go.mod" (
  echo [gotunnel] ERROR: go.mod not found under "%ROOT_DIR%" 1>&2
  echo [gotunnel] Please run this script from the gotunnel project copy: gotunnel\scripts\start-client.cmd 1>&2
  echo [gotunnel] Or copy the whole gotunnel directory to Windows, not just this .cmd file. 1>&2
  exit /b 1
)

if /I "%GO_RUN%"=="true" (
  echo [gotunnel] starting client with go run
  echo   server: %SERVER_ADDR%
  echo   local : %LOCAL_ADDR%
  echo   tls   : %USE_TLS%
  echo   debug : %DEBUG%
  pushd "%ROOT_DIR%" >nul
  go run ./client -server "%SERVER_ADDR%" -local "%LOCAL_ADDR%" -tls=%USE_TLS% -debug=%DEBUG%
  set "EXIT_CODE=%ERRORLEVEL%"
  popd >nul
  exit /b %EXIT_CODE%
)

if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"
echo [gotunnel] building client -^> "%BIN%"
pushd "%ROOT_DIR%" >nul
go build -o "%BIN%" ./client
if errorlevel 1 (
  popd >nul
  exit /b 1
)
popd >nul

if /I "%BUILD_ONLY%"=="true" (
  echo [gotunnel] build complete
  exit /b 0
)

echo [gotunnel] starting client
echo   server: %SERVER_ADDR%
echo   local : %LOCAL_ADDR%
echo   tls   : %USE_TLS%
echo   debug : %DEBUG%
"%BIN%" -server "%SERVER_ADDR%" -local "%LOCAL_ADDR%" -tls=%USE_TLS% -debug=%DEBUG%
exit /b %ERRORLEVEL%

:help
call :print_help
exit /b 0

:help_error
call :print_help
exit /b 1

:print_help
echo Usage:
echo   %~nx0 [options]
echo.
echo Options:
echo   -s, --server ADDR   gotunnel server control address, default: %SERVER_ADDR%
echo   -l, --local ADDR    local service address to expose, default: %LOCAL_ADDR%
echo       --tls           connect with TLS
echo       --debug         enable debug logs
echo       --build-only    only build client binary
echo       --go-run        run with go run ./client directly, do not build exe
echo   -h, --help          show help
echo.
echo Environment variables:
echo   GOTUNNEL_SERVER_ADDR=%SERVER_ADDR%
echo   GOTUNNEL_LOCAL_ADDR=%LOCAL_ADDR%
echo   GOTUNNEL_TLS=%USE_TLS%
echo   GOTUNNEL_DEBUG=%DEBUG%
echo.
echo Demos:
echo   REM Expose local VNC 5900 through a server running on 1.2.3.4:6000
echo   %~nx0 --server 1.2.3.4:6000 --local 127.0.0.1:5900
echo.
echo   REM Run from source without building exe
echo   %~nx0 --go-run --server 1.2.3.4:6000 --local 10.x.x.x:43080
echo.
echo   REM Expose local RDP 3389
echo   %~nx0 -s 1.2.3.4:6000 -l 127.0.0.1:3389
echo.
echo   REM Use environment variables in cmd.exe
echo   set GOTUNNEL_SERVER_ADDR=1.2.3.4:6000
echo   set GOTUNNEL_LOCAL_ADDR=127.0.0.1:8080
echo   %~nx0
exit /b 0
