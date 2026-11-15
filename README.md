# Система доставки еды на микросервисах

Курсовая работа по дисциплине «Настройка и администрирование сервисного
программного обеспечения». Тема: настройка и администрирование
инфраструктуры для системы доставки еды на основе микросервисов.

Оператор принимает заказы, ресторан готовит, курьер доставляет.
Сервисы передают друг другу заказ через события Kafka, а развёртываются
в кластер Kubernetes (k3s) из трёх узлов с помощью Helm.

## Состав

| Компонент | Назначение |
|---|---|
| `gateway` | API-шлюз: вход, JWT, роли, ограничение частоты запросов |
| `order-service` | Приём заказов, статусы и история |
| `restaurant-service` | Рестораны, меню, расчёт стоимости, кухня |
| `payment-service` | Авторизация и возврат платежей |
| `courier-service` | Смены курьеров, назначение и доставка |
| `notification-service` | Поток событий в панель оператора (SSE) |
| `loadgen` | Генератор заказов (CronJob) |
| `web` | Панель оператора (nginx) |
| PostgreSQL 16 | Отдельная схема и роль для каждого сервиса |
| Kafka 3.8 (KRaft) | Топики `orders`, `payments`, `kitchen`, `delivery` |
| Prometheus, Grafana | Метрики и дашборд |

Жизненный цикл заказа:

```
order.created -> payment.authorized -> kitchen.accepted -> kitchen.ready
              -> courier.assigned -> delivery.completed
```

Каждый сервис записывает событие в таблицу `outbox` в одной транзакции
с изменением данных, а фоновая задача переносит его в Kafka. Получатели
отмечают обработанные события в `processed_events`, поэтому повторная
доставка не меняет данные дважды.

## Структура

```
cmd/<сервис>/          исходный код и миграции каждого сервиса
internal/platform/     общий каркас: конфигурация, БД, HTTP, outbox
internal/events/       события и работа с Kafka
internal/auth/         JWT и хеширование паролей
internal/metrics/      метрики в формате Prometheus
web/                   панель оператора и конфигурация nginx
deploy/helm/           Helm-чарт для Kubernetes
deploy/k3d/            описание кластера из трёх узлов
deploy/postgres/       создание ролей и схем
deploy/monitoring/     Prometheus и Grafana для Docker Compose
scripts/               создание кластера, развёртывание, проверка
.github/workflows/     конвейер CI/CD и демонстрационный прогон
Dockerfile             общий образ сервисов на Go (аргумент SERVICE)
docker-compose.yml     локальный запуск
```

## Локальный запуск (Docker Compose)

```bash
cp .env.example .env          # задать свои пароли
docker compose up -d --build
docker compose --profile load up -d loadgen   # по желанию: поток заказов
```

Панель: http://localhost:8080 (логины `operator` и `admin`, пароли из `.env`),
Grafana: http://localhost:3000, Prometheus: http://localhost:9090.

## Кластер Kubernetes

Нужны Docker, [k3d](https://k3d.io), kubectl и Helm.

```bash
scripts/cluster-up.sh                 # управляющий узел и два рабочих
LOCAL_BUILD=1 scripts/deploy.sh       # сборка образов и установка чарта
scripts/smoke-test.sh                 # приёмочная проверка
```

Панель открывается на http://localhost:8080. Пароли создаются случайно
и хранятся в секрете `food-delivery-secrets`:

```bash
kubectl -n food get secret food-delivery-secrets \
  -o jsonpath='{.data.operator-password}' | base64 -d
kubectl -n food port-forward svc/grafana 3000:3000
```

Удаление кластера: `scripts/cluster-down.sh`.

## CI/CD

`.github/workflows/ci-cd.yml` при каждом push в `main`:

1. проверяет форматирование, запускает `go vet` и модульные тесты,
   проверяет Helm-чарт (`helm lint`, `kubeconform`) и `docker-compose.yml`;
2. собирает восемь образов и публикует их в GitHub Container Registry
   с тегами SHA коммита и `latest`;
3. создаёт кластер k3d из трёх узлов, устанавливает чарт с образами
   этого коммита и выполняет `scripts/smoke-test.sh`.

## Сетевая изоляция

В пространстве имён `food` входящий трафик запрещён по умолчанию.
Разрешены только пути: входной контроллер -> `web` и `gateway`,
`gateway` -> прикладные сервисы, `order-service` -> `restaurant-service`,
поды с меткой `db-access` -> PostgreSQL, поды с меткой `kafka-access` ->
Kafka, Prometheus -> метрики сервисов.
