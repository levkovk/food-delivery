#!/usr/bin/env bash
# Удаляет кластер k3d вместе с данными.
set -euo pipefail
k3d cluster delete food
