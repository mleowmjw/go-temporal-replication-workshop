-- =============================================================================
-- Blue-Green Workshop init script — PostgreSQL 18
-- =============================================================================
-- This is the ORIGINAL schema (before any migration).
-- The blue-green workshop scenario renames full_name → display_name
-- and adds the phone column using the expand/contract pattern.
-- =============================================================================

CREATE ROLE dbz WITH LOGIN REPLICATION PASSWORD 'dbz';
GRANT CONNECT ON DATABASE appdb TO dbz;

\c appdb;

-- =============================================================================
-- Schema
-- =============================================================================
CREATE SCHEMA IF NOT EXISTS inventory;

-- customers: ORIGINAL schema with full_name.
-- The workshop expand SQL will add display_name + phone.
-- The workshop contract SQL will drop full_name once the new app is live.
CREATE TABLE inventory.customers (
  id         BIGSERIAL   PRIMARY KEY,
  email      TEXT        NOT NULL UNIQUE,
  full_name  TEXT        NOT NULL,
  status     TEXT        NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- PG18: stored generated column — replicated when publication uses
  -- publish_generated_columns = stored.
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
-- Seed data — original rows with full_name only
-- =============================================================================
INSERT INTO inventory.customers (email, full_name)
VALUES
  ('alice@example.com', 'Alice Tan'),
  ('bob@example.com',   'Bob Lee'),
  ('carol@example.com', 'Carol Wang');

INSERT INTO inventory.orders (customer_id, amount_cents, currency, status)
VALUES
  (1, 1200, 'USD', 'paid'),
  (2, 4500, 'USD', 'pending'),
  (3, 8900, 'SGD', 'paid');

-- =============================================================================
-- Privileges
-- =============================================================================
GRANT USAGE ON SCHEMA inventory TO dbz;
GRANT pg_read_all_data TO dbz;

-- =============================================================================
-- Publication — PG18: publish_generated_columns = stored
-- =============================================================================
CREATE PUBLICATION workshop_bluegreen_pub
  FOR TABLE inventory.customers, inventory.orders
  WITH (publish_generated_columns = stored);

-- =============================================================================
-- Replication slot — PG18: failover = true
-- =============================================================================
SELECT pg_create_logical_replication_slot(
  'workshop_bluegreen_slot',
  'pgoutput',
  false,   -- temporary
  false,   -- two_phase
  true     -- failover (PG17+/PG18)
);
