#!/usr/bin/env bash
# Развёртывает систему в кластер с помощью Helm.
#   LOCAL_BUILD=1 ./scripts/deploy.sh      - собрать образы и загрузить в k3d
#   REGISTRY=ghcr.io/user TAG=sha ./scripts/deploy.sh - образы из реестра
set -euo pipefail
cd "$(dirname "$0")/.."

NAMESPACE=${NAMESPACE:-food}
REGISTRY=${REGISTRY:-local}
TAG=${TAG:-dev}
PULL_SECRET=${PULL_SECRET:-}
SERVICES="gateway order restaurant payment courier notification loadgen"

if [[ "${LOCAL_BUILD:-0}" == "1" ]]; then
    REGISTRY=local
    for s in $SERVICES; do
        docker build -q --build-arg SERVICE="$s" -t "local/food-delivery-$s:$TAG" .
    done
    docker build -q -t "local/food-delivery-web:$TAG" web
    images=$(for s in $SERVICES web; do echo "local/food-delivery-$s:$TAG"; done)
    k3d image import -c food $images
fi

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$NAMESPACE" pod-security.kubernetes.io/enforce=baseline \
    pod-security.kubernetes.io/warn=restricted --overwrite >/dev/null

# Секреты создаются один раз со случайными значениями и при повторном
# развёртывании не меняются. В репозитории паролей нет.
if ! kubectl -n "$NAMESPACE" get secret food-delivery-secrets >/dev/null 2>&1; then
    rnd() { openssl rand -base64 24 | tr -d '/+=' ; }
    kubectl -n "$NAMESPACE" create secret generic food-delivery-secrets \
        --from-literal=postgres-password="$(rnd)" \
        --from-literal=gateway-db-password="$(rnd)" \
        --from-literal=order-db-password="$(rnd)" \
        --from-literal=restaurant-db-password="$(rnd)" \
        --from-literal=payment-db-password="$(rnd)" \
        --from-literal=courier-db-password="$(rnd)" \
        --from-literal=jwt-secret="$(openssl rand -hex 32)" \
        --from-literal=admin-password="${ADMIN_PASSWORD:-$(rnd)}" \
        --from-literal=operator-password="${OPERATOR_PASSWORD:-$(rnd)}" \
        --from-literal=grafana-password="$(rnd)"
    echo "Создан секрет food-delivery-secrets"
fi

helm upgrade --install food-delivery deploy/helm/food-delivery \
    --namespace "$NAMESPACE" \
    --set image.registry="$REGISTRY" \
    --set image.tag="$TAG" \
    --set image.pullSecret="$PULL_SECRET" \
    --set image.pullPolicy="$([[ $REGISTRY == local ]] && echo Never || echo IfNotPresent)" \
    --wait --timeout "${DEPLOY_TIMEOUT:-8m}"

kubectl -n "$NAMESPACE" get pods -o wide
echo
echo "Панель: http://localhost:8080"
echo "Пароль оператора: kubectl -n $NAMESPACE get secret food-delivery-secrets -o jsonpath='{.data.operator-password}' | base64 -d"
