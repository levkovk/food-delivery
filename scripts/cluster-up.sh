#!/usr/bin/env bash
# Создаёт кластер k3d из трёх узлов (если его ещё нет).
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v k3d >/dev/null; then
    echo "Установите k3d: https://k3d.io (brew install k3d / choco install k3d)" >&2
    exit 1
fi
if k3d cluster list food >/dev/null 2>&1; then
    echo "Кластер food уже существует"
else
    k3d cluster create --config deploy/k3d/cluster.yaml
fi
kubectl config use-context k3d-food >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout=180s
kubectl get nodes -L tier
