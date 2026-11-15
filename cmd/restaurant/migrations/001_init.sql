CREATE TABLE restaurants (
    id           bigserial PRIMARY KEY,
    name         text    NOT NULL UNIQUE,
    cuisine      text    NOT NULL,
    zone         text    NOT NULL,
    prep_minutes int     NOT NULL CHECK (prep_minutes BETWEEN 1 AND 120),
    active       boolean NOT NULL DEFAULT true
);

CREATE TABLE menu_items (
    id            bigserial PRIMARY KEY,
    restaurant_id bigint  NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
    name          text    NOT NULL,
    price         bigint  NOT NULL CHECK (price > 0),
    available     boolean NOT NULL DEFAULT true,
    UNIQUE (restaurant_id, name)
);

CREATE TABLE tickets (
    order_id      bigint PRIMARY KEY,
    restaurant_id bigint      NOT NULL,
    zone          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'cooking',
    ready_at      timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tickets_due_idx ON tickets (status, ready_at);
