#!/usr/bin/env bash
# Reconstruit poste/bin/mulcv-scan.exe de facon reproductible (meme version de Go que la CI : 1.24.7).
set -euo pipefail
cd "$(dirname "$0")/src"
go test ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o ../poste/bin/mulcv-scan.exe .
sha256sum ../poste/bin/mulcv-scan.exe
