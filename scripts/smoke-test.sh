#!/usr/bin/env bash
# Проверка развёрнутой системы через входной контроллер: вход, права,
# создание заказа и его прохождение по всей цепочке событий.
set -euo pipefail
BASE=${BASE:-http://localhost:8080}
NAMESPACE=${NAMESPACE:-food}
secret() { kubectl -n "$NAMESPACE" get secret food-delivery-secrets -o jsonpath="{.data.$1}" | base64 -d; }
OPERATOR_PASSWORD=${OPERATOR_PASSWORD:-$(secret operator-password)}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-$(secret admin-password)}

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK   $*"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
login() {
    curl -sf -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
        -d "{\"username\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'
}

for i in $(seq 1 30); do
    [[ $(code "$BASE/") == 200 ]] && break
    sleep 2
done
[[ $(code "$BASE/") == 200 ]] || fail "panel is not available"
ok "панель открывается"

[[ $(code "$BASE/api/orders") == 401 ]] || fail "request without token must be 401"
ok "запрос без токена отклонён (401)"
[[ $(code -X POST "$BASE/api/auth/login" -d '{"username":"operator","password":"wrong"}') == 401 ]] || fail "wrong password"
ok "неверный пароль отклонён (401)"

OP=$(login operator "$OPERATOR_PASSWORD") || fail "operator login"
AD=$(login admin "$ADMIN_PASSWORD") || fail "admin login"
ok "вход оператора и администратора"
H_OP=(-H "Authorization: Bearer $OP" -H 'Content-Type: application/json')
H_AD=(-H "Authorization: Bearer $AD" -H 'Content-Type: application/json')

[[ $(code "${H_OP[@]}" "$BASE/api/payments") == 403 ]] || fail "operator must not see payments"
[[ $(code "${H_OP[@]}" -X POST "$BASE/api/restaurants" -d '{}') == 403 ]] || fail "operator must not edit restaurants"
[[ $(code "${H_AD[@]}" "$BASE/api/payments") == 200 ]] || fail "admin payments"
ok "разграничение ролей (403 для оператора, 200 для администратора)"
[[ $(code "${H_AD[@]}" -X POST "$BASE/api/internal/quote" -d '{}') == 404 ]] || fail "internal route exposed"
ok "внутренний метод недоступен снаружи (404)"

ITEM=$(curl -sf "${H_OP[@]}" "$BASE/api/restaurants/1/menu" | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["id"])')
ORDER=$(curl -sf "${H_OP[@]}" -X POST "$BASE/api/orders" \
    -d "{\"restaurant_id\":1,\"items\":[{\"item_id\":$ITEM,\"qty\":3}],\"address\":\"ул. Проверочная, 1\"}" \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])') || fail "create order"
ok "заказ № $ORDER создан"

status=""
for i in $(seq 1 90); do
    status=$(curl -sf "${H_OP[@]}" "$BASE/api/orders/$ORDER" | python3 -c 'import sys,json;print(json.load(sys.stdin)["order"]["status"])')
    [[ $status == delivered ]] && break
    sleep 2
done
[[ $status == delivered ]] || fail "order $ORDER stuck in status $status"
ok "заказ прошёл цепочку created -> paid -> cooking -> ready -> delivering -> delivered"

BIG=$(curl -sf "${H_OP[@]}" -X POST "$BASE/api/orders" \
    -d "{\"restaurant_id\":1,\"items\":[{\"item_id\":$ITEM,\"qty\":50}],\"address\":\"ул. Проверочная, 2\"}" \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
for i in $(seq 1 30); do
    status=$(curl -sf "${H_OP[@]}" "$BASE/api/orders/$BIG" | python3 -c 'import sys,json;print(json.load(sys.stdin)["order"]["status"])')
    [[ $status == declined ]] && break
    sleep 1
done
[[ $status == declined ]] || fail "order over limit must be declined, got $status"
ok "заказ сверх лимита отклонён платёжным сервисом"

echo "Все проверки пройдены"
