# Canvas 1 — Section 1: DB setup + schema

## Goal
Make Postgres CDC-ready with the fewest possible moving parts.

## Audience promise
By the end of this section, attendees will have:
- a running Postgres container
- logical replication enabled
- a tiny `inventory` schema
- one successful read/write proof

## Speaker script
Use this section to establish the rule that **CDC starts with the database, not with Debezium**.

Say:
1. Logical replication begins with WAL settings.
2. We keep the schema intentionally tiny so the room can follow every change.
3. The only proof we need here is: a `select` works, and an `insert` lands.

## Files
### `01-db/compose.yml`
```yaml
services:
  postgres:
    image: postgres:16
    container_name: ws-postgres
    ports:
      - "5432:5432"
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: appdb
    command:
      - postgres
      - -c
      - wal_level=logical
      - -c
      - max_wal_senders=10
      - -c
      - max_replication_slots=10
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./init:/docker-entrypoint-initdb.d

volumes:
  pgdata:
```

### `01-db/init/001-init.sql`
```sql
CREATE ROLE dbz WITH LOGIN REPLICATION PASSWORD 'dbz';
GRANT CONNECT ON DATABASE appdb TO dbz;

\c appdb;

CREATE SCHEMA IF NOT EXISTS inventory;

CREATE TABLE inventory.customers (
  id         BIGSERIAL PRIMARY KEY,
  email      TEXT NOT NULL UNIQUE,
  full_name  TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE inventory.orders (
  id           BIGSERIAL PRIMARY KEY,
  customer_id  BIGINT NOT NULL REFERENCES inventory.customers(id),
  amount_cents BIGINT NOT NULL,
  currency     TEXT NOT NULL DEFAULT 'USD',
  status       TEXT NOT NULL DEFAULT 'pending',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO inventory.customers (email, full_name)
VALUES
  ('alice@example.com', 'Alice Tan'),
  ('bob@example.com', 'Bob Lee');

INSERT INTO inventory.orders (customer_id, amount_cents, currency, status)
VALUES
  (1, 1200, 'USD', 'paid'),
  (2, 4500, 'USD', 'pending');

GRANT USAGE ON SCHEMA inventory TO dbz;
GRANT SELECT ON ALL TABLES IN SCHEMA inventory TO dbz;
ALTER DEFAULT PRIVILEGES IN SCHEMA inventory GRANT SELECT ON TABLES TO dbz;
```

## Commands to run live
```bash
cd 01-db
docker compose up -d

psql "postgresql://postgres:postgres@localhost:5432/appdb" -c "select * from inventory.customers"
psql "postgresql://postgres:postgres@localhost:5432/appdb" -c "insert into inventory.customers (email, full_name) values ('charlie@example.com','Charlie Ng')"
```

## Success check
- `select * from inventory.customers` returns rows
- a new row inserts without errors

## What not to do in this section
- do not introduce Kafka yet
- do not explain publications/slots in depth
- do not troubleshoot downstream tooling here

## Transition line to Section 2
“Now that Postgres is producing a trustworthy source of truth, we can attach Debezium and watch the change stream move.”
