#!/usr/bin/env bash
set -euo pipefail

npm --prefix ui run build
cp ui/dist/index.html internal/server/dashboard.html
