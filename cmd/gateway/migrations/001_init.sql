CREATE TABLE users (
    id            bigserial PRIMARY KEY,
    username      text        NOT NULL UNIQUE,
    role          text        NOT NULL CHECK (role IN ('admin', 'operator')),
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE login_audit (
    id       bigserial PRIMARY KEY,
    username text        NOT NULL,
    success  boolean     NOT NULL,
    client   text        NOT NULL,
    at       timestamptz NOT NULL DEFAULT now()
);
