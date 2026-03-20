-- =============================================================================
-- Session-1 init script — PostgreSQL 18 CDC-ready setup
-- =============================================================================
-- Role for Debezium / CDC connector (minimum privileges)
CREATE ROLE dbz WITH LOGIN REPLICATION PASSWORD 'dbz';
GRANT CONNECT ON DATABASE appdb TO dbz;

\c appdb;

-- =============================================================================
-- Schema
-- =============================================================================
CREATE SCHEMA IF NOT EXISTS inventory;

-- customers: includes a PG18 STORED generated column for full-text search key.
-- Stored generated columns can now be replicated (PG18 feature).
CREATE TABLE inventory.customers (
  id         BIGSERIAL   PRIMARY KEY,
  email      TEXT        NOT NULL UNIQUE,
  full_name  TEXT        NOT NULL,
  status     TEXT        NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- PG18: stored generated column — computed on write, replicated to subscribers
  -- when the publication uses publish_generated_columns = stored.
  search_key TEXT GENERATED ALWAYS AS (lower(full_name) || ' ' || lower(email)) STORED
);

CREATE TABLE inventory.orders (
  id           BIGSERIAL   PRIMARY KEY,
  customer_id  BIGINT      NOT NULL REFERENCES inventory.customers(id),
  amount_cents BIGINT      NOT NULL,
  currency     TEXT        NOT NULL DEFAULT 'USD',
  status       TEXT        NOT NULL DEFAULT 'pending',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- =============================================================================
-- Seed data
-- =============================================================================
INSERT INTO inventory.customers (email, full_name)
VALUES
  ('alice@example.com', 'Alice Tan'),
  ('bob@example.com',   'Bob Lee');

INSERT INTO inventory.orders (customer_id, amount_cents, currency, status)
VALUES
  (1, 1200, 'USD', 'paid'),
  (2, 4500, 'USD', 'pending');

-- =============================================================================
-- Privileges
-- =============================================================================
-- pg_read_all_data is a predefined role (PG14+) — cleaner than per-table grants.
GRANT USAGE ON SCHEMA inventory TO dbz;
GRANT pg_read_all_data TO dbz;

-- =============================================================================
-- Publication — PG18: publish_generated_columns = stored
-- Pre-creating the publication here means Debezium (session-2) picks it up as-is
-- without needing publication.autocreate.mode to create a new one.
-- =============================================================================
CREATE PUBLICATION workshop_inventory_pub
  FOR TABLE inventory.customers, inventory.orders
  WITH (publish_generated_columns = stored);

-- =============================================================================
-- Replication slot — PG17+/PG18: failover = true
-- A failover-capable slot is synchronized to physical standbys, allowing
-- subscribers to resume after a primary failover without data loss.
-- =============================================================================
SELECT pg_create_logical_replication_slot(
  'workshop_inventory_slot',
  'pgoutput',
  false,   -- temporary
  false,   -- two_phase
  true     -- failover (PG17+/PG18)
);
