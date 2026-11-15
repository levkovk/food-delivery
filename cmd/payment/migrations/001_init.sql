CREATE TABLE payments (
    id         bigserial PRIMARY KEY,
    order_id   bigint      NOT NULL UNIQUE,
    amount     bigint      NOT NULL,
    status     text        NOT NULL,
    reason     text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
