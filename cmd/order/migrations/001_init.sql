CREATE TABLE orders (
    id              bigserial PRIMARY KEY,
    restaurant_id   bigint      NOT NULL,
    restaurant_name text        NOT NULL,
    items           jsonb       NOT NULL,
    total           bigint      NOT NULL CHECK (total >= 0),
    zone            text        NOT NULL,
    address         text        NOT NULL,
    status          text        NOT NULL DEFAULT 'created',
    courier_name    text,
    created_by      text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_status_idx ON orders (status, created_at DESC);

CREATE TABLE order_history (
    id         bigserial PRIMARY KEY,
    order_id   bigint      NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    status     text        NOT NULL,
    event_type text        NOT NULL,
    note       text,
    at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_history_order_idx ON order_history (order_id, at);
