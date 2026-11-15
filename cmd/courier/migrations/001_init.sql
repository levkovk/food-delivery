CREATE TABLE couriers (
    id         bigserial PRIMARY KEY,
    name       text    NOT NULL,
    zone       text    NOT NULL,
    transport  text    NOT NULL,
    on_shift   boolean NOT NULL DEFAULT true,
    busy       boolean NOT NULL DEFAULT false,
    deliveries int     NOT NULL DEFAULT 0
);
CREATE INDEX couriers_free_idx ON couriers (zone) WHERE on_shift AND NOT busy;

CREATE TABLE deliveries (
    order_id    bigint PRIMARY KEY,
    zone        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'waiting',
    courier_id  bigint REFERENCES couriers(id),
    attempts    int         NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    assigned_at timestamptz,
    eta_at      timestamptz,
    done_at     timestamptz
);
CREATE INDEX deliveries_status_idx ON deliveries (status, created_at);

INSERT INTO couriers (name, zone, transport) VALUES
    ('Алексей Орлов',    'center', 'велосипед'),
    ('Марина Соколова',  'center', 'пешком'),
    ('Илья Кузнецов',    'center', 'самокат'),
    ('Дарья Волкова',    'north',  'автомобиль'),
    ('Сергей Лебедев',   'north',  'велосипед'),
    ('Ольга Новикова',   'north',  'самокат'),
    ('Павел Морозов',    'south',  'автомобиль'),
    ('Анна Павлова',     'south',  'велосипед'),
    ('Никита Фёдоров',   'south',  'самокат');
