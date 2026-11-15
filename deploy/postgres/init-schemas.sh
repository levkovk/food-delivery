#!/bin/sh
# Создаёт для каждого сервиса отдельную роль и схему (шаблон database per
# service в пределах одного экземпляра PostgreSQL). Сервис видит только свою
# схему: чужие таблицы ему недоступны даже при ошибке в коде.
set -eu

create() {
    role="$1"
    password="$2"
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
        -v role="$role" -v password="$password" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'role', :'password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
SELECT format('CREATE SCHEMA IF NOT EXISTS %I AUTHORIZATION %I', :'role', :'role') \gexec
SELECT format('ALTER ROLE %I SET search_path = %I', :'role', :'role') \gexec
SQL
}

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -c "REVOKE ALL ON SCHEMA public FROM PUBLIC"

create gateway     "$GATEWAY_DB_PASSWORD"
create orders      "$ORDER_DB_PASSWORD"
create restaurants "$RESTAURANT_DB_PASSWORD"
create payments    "$PAYMENT_DB_PASSWORD"
create couriers    "$COURIER_DB_PASSWORD"
