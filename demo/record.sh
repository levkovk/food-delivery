#!/usr/bin/env bash
# Демонстрационный прогон для отчёта: запуск в Docker Compose, затем
# кластер k3d из трёх узлов, проверка сетевых политик, масштабирования и
# отказа узла. Снимки окон сохраняются в demo/out.
#   xvfb-run -a -s '-screen 0 2200x1600x24' demo/record.sh
set -euo pipefail
cd "$(dirname "$0")/.."
source demo/console.sh
OUT="$PWD/demo/out"
mkdir -p "$OUT"
export OPERATOR_PASSWORD="${OPERATOR_PASSWORD:-operator-demo-pass}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin-demo-pass}"
title() { echo; echo "######## $* ########"; }

# 1. Локальный запуск в Docker Compose
title "Docker Compose"
sed -e "s/^ADMIN_PASSWORD=.*/ADMIN_PASSWORD=$ADMIN_PASSWORD/" \
    -e "s/^OPERATOR_PASSWORD=.*/OPERATOR_PASSWORD=$OPERATOR_PASSWORD/" .env.example > .env
docker compose --profile load up -d --build --wait
sleep 30
open_console 120 20
cmd 4 "docker compose ps --format 'table {{.Service}}\t{{.Image}}\t{{.Status}}'"
snap "$OUT/compose-ps.png"
close_console
docker compose --profile load down -v

# 2. Кластер k3d и развёртывание Helm
title "Кластер"
bash scripts/cluster-up.sh
LOCAL_BUILD=1 bash scripts/deploy.sh
bash scripts/smoke-test.sh | tee "$OUT/smoke.txt"
echo "Ожидание заказов от генератора нагрузки"
sleep 200

open_console 132 30
cmd 3 "kubectl get nodes -L tier"
cmd 4 "kubectl -n food get pods -o custom-columns=POD:.metadata.name,STATUS:.status.phase,NODE:.spec.nodeName --sort-by=.spec.nodeName"
snap "$OUT/pods.png"
close_console

open_console 132 34
cmd 4 "kubectl -n food get deploy,statefulset,hpa,cronjob"
cmd 3 "kubectl -n food get ingress"
snap "$OUT/resources.png"
close_console

open_console 132 18
cmd 3 "helm list -n food"
cmd 3 "helm history food-delivery -n food"
snap "$OUT/helm.png"
close_console

title "Сетевые политики"
kubectl -n food run warmup --image=busybox:1.36 --restart=Never --rm -i -- true >/dev/null 2>&1 || true
open_console 132 26
cmd 3 "kubectl -n food get networkpolicy"
cmd 9 "kubectl -n food run probe --rm -i --restart=Never --image=busybox:1.36 -- nc -zv -w 3 postgres 5432"
cmd 9 "kubectl -n food run probe --rm -i --restart=Never --image=busybox:1.36 -l db-access=true -- nc -zv -w 3 postgres 5432"
cmd 9 "kubectl -n food run probe --rm -i --restart=Never --image=busybox:1.36 -- nc -zv -w 3 order-service 8080"
snap "$OUT/netpol.png"
close_console

title "Веб-интерфейсы"
python demo/browser.py panel http://localhost:8080 "$OUT" "$OPERATOR_PASSWORD" "$ADMIN_PASSWORD" || echo "!! панель"
kubectl -n food port-forward svc/grafana 3000:3000 >/dev/null 2>&1 &
PF=$!
sleep 4
GRAFANA_PASSWORD=$(kubectl -n food get secret food-delivery-secrets -o jsonpath='{.data.grafana-password}' | base64 -d)
python demo/browser.py grafana http://localhost:3000 "$OUT/grafana.png" "$GRAFANA_PASSWORD" || echo "!! grafana"
kill $PF || true

title "Масштабирование"
open_console 132 24
cmd 3 "kubectl -n food scale deployment courier-service --replicas=4"
cmd 25 "kubectl -n food rollout status deployment courier-service"
cmd 3 "kubectl -n food get pods -l app.kubernetes.io/name=courier-service -o wide"
snap "$OUT/scale.png"
close_console

title "Отказ узла"
open_console 132 34
cmd 4 "docker stop k3d-food-agent-1"
cmd 100 "sleep 95; kubectl get nodes"
cmd 4 "kubectl -n food get pods -o wide -l 'app.kubernetes.io/name in (gateway,order-service,courier-service)'"
snap "$OUT/node-failure.png"
close_console
docker start k3d-food-agent-1
echo "Готово: $(ls "$OUT")"
