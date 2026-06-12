-- +goose Up
CREATE TABLE lots (
    -- owner_id references users.id logically, but there is no FK across
    -- feature boundaries: a foreign key would couple this migration to
    -- the users feature and break rule 1 (a feature must be deletable
    -- by deleting its folder). Ownership integrity is enforced in the
    -- service through users.UserExists.
    id                  uuid PRIMARY KEY,
    owner_id            uuid NOT NULL,
    title               text NOT NULL,
    description         text NOT NULL DEFAULT '',
    start_price_minor   bigint NOT NULL CHECK (start_price_minor > 0),
    currency            text NOT NULL CHECK (char_length(currency) = 3),
    current_price_minor bigint NOT NULL,
    bid_count           integer NOT NULL DEFAULT 0,
    ends_at             timestamptz NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- The listing endpoint serves only active lots ordered by closing time.
CREATE INDEX lots_ends_at_idx ON lots (ends_at);

-- +goose Down
DROP TABLE lots;
