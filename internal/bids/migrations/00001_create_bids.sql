-- +goose Up
CREATE TABLE bids (
    -- lot_id and bidder_id reference rows of other features logically,
    -- but carry no FK: cross-feature foreign keys would break rule 1
    -- (features must be deletable folder-by-folder). Consistency is
    -- enforced in lots.PlaceBidTx, in the same transaction as the
    -- insert into this table.
    id           uuid PRIMARY KEY,
    lot_id       uuid NOT NULL,
    bidder_id    uuid NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Bid history is always read per lot, newest first.
CREATE INDEX bids_lot_id_created_at_idx ON bids (lot_id, created_at DESC);

-- +goose Down
DROP TABLE bids;
