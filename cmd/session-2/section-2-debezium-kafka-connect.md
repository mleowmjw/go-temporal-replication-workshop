# Canvas 2 — Section 2: Debezium + Kafka Connect

## Goal
Submit one working Postgres source connector and watch change events appear in Kafka topics.

## Audience promise
By the end of this section, attendees will have:
- Kafka running locally
- Kafka Connect running locally
- one Debezium Postgres connector registered
- CDC events visible in a topic consumer

## Speaker script
Say:
1. Debezium snapshots first, then streams changes.
2. Kafka Connect is controlled over REST.
3. We keep the connector config readable in YAML, even though the API submission is JSON.
4. For the workshop, we stop once the topic consumer shows the change events.

## Files
### `02-cdc/compose.yml`
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
      - ../01-db/init:/docker-entrypoint-initdb.d

  kafka:
    image: bitnami/kafka:3.7
    container_name: ws-kafka
    ports:
      - "9092:9092"
      - "29092:29092"
    environment:
      KAFKA_CFG_NODE_ID: "1"
      KAFKA_CFG_PROCESS_ROLES: "broker,controller"
      KAFKA_CFG_CONTROLLER_LISTENER_NAMES: "CONTROLLER"
      KAFKA_CFG_LISTENERS: "PLAINTEXT://:9092,CONTROLLER://:9093,PLAINTEXT_HOST://:29092"
      KAFKA_CFG_ADVERTISED_LISTENERS: "PLAINTEXT://kafka:9092,PLAINTEXT_HOST://localhost:29092"
      KAFKA_CFG_LISTENER_SECURITY_PROTOCOL_MAP: "PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT,PLAINTEXT_HOST:PLAINTEXT"
      KAFKA_CFG_CONTROLLER_QUORUM_VOTERS: "1@kafka:9093"
      KAFKA_CFG_INTER_BROKER_LISTENER_NAME: "PLAINTEXT"
      KAFKA_CFG_AUTO_CREATE_TOPICS_ENABLE: "true"
      ALLOW_PLAINTEXT_LISTENER: "yes"

  connect:
    image: debezium/connect:3.3
    container_name: ws-connect
    depends_on:
      - kafka
      - postgres
    ports:
      - "8083:8083"
    environment:
      BOOTSTRAP_SERVERS: kafka:9092
      GROUP_ID: "1"
      CONFIG_STORAGE_TOPIC: connect_configs
      OFFSET_STORAGE_TOPIC: connect_offsets
      STATUS_STORAGE_TOPIC: connect_statuses
      CONFIG_STORAGE_REPLICATION_FACTOR: "1"
      OFFSET_STORAGE_REPLICATION_FACTOR: "1"
      STATUS_STORAGE_REPLICATION_FACTOR: "1"
      KEY_CONVERTER: org.apache.kafka.connect.json.JsonConverter
      VALUE_CONVERTER: org.apache.kafka.connect.json.JsonConverter
      KEY_CONVERTER_SCHEMAS_ENABLE: "false"
      VALUE_CONVERTER_SCHEMAS_ENABLE: "false"
      CONNECT_REST_ADVERTISED_HOST_NAME: connect

volumes:
  pgdata:
```

### `02-cdc/connectors/inventory-connector.yaml`
```yaml
name: inventory-connector
config:
  connector.class: io.debezium.connector.postgresql.PostgresConnector
  database.hostname: postgres
  database.port: "5432"
  database.user: dbz
  database.password: dbz
  database.dbname: appdb
  topic.prefix: workshop
  plugin.name: pgoutput
  slot.name: workshop_inventory_slot
  publication.name: workshop_inventory_pub
  publication.autocreate.mode: filtered
  schema.include.list: inventory
  table.include.list: inventory.customers,inventory.orders
  snapshot.mode: initial
  include.schema.changes: "false"
```

### `02-cdc/connectors/inventory-connector.json`
```json
{
  "name": "inventory-connector",
  "config": {
    "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
    "database.hostname": "postgres",
    "database.port": "5432",
    "database.user": "dbz",
    "database.password": "dbz",
    "database.dbname": "appdb",
    "topic.prefix": "workshop",
    "plugin.name": "pgoutput",
    "slot.name": "workshop_inventory_slot",
    "publication.name": "workshop_inventory_pub",
    "publication.autocreate.mode": "filtered",
    "schema.include.list": "inventory",
    "table.include.list": "inventory.customers,inventory.orders",
    "snapshot.mode": "initial",
    "include.schema.changes": "false"
  }
}
```

## Commands to run live
```bash
cd 02-cdc
docker compose up -d

curl http://localhost:8083/connector-plugins

curl -X POST \
  -H "Content-Type: application/json" \
  --data @connectors/inventory-connector.json \
  http://localhost:8083/connectors

curl http://localhost:8083/connectors/inventory-connector/status
```

### Consume CDC events
```bash
docker exec -it ws-kafka /opt/bitnami/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 \
  --topic workshop.inventory.customers \
  --from-beginning
```

### Generate fresh changes
```bash
psql "postgresql://postgres:postgres@localhost:5432/appdb" <<'SQL'
INSERT INTO inventory.customers (email, full_name) VALUES ('dina@example.com', 'Dina Ong');
UPDATE inventory.orders SET status = 'paid' WHERE id = 1;
DELETE FROM inventory.orders WHERE id = 2;
SQL
```

## Success check
- connector status is `RUNNING`
- topic consumer shows the initial snapshot rows
- topic consumer shows the live insert/update/delete changes

## What not to do in this section
- do not add Schema Registry to the live path
- do not add sink connectors yet
- do not detour into partitioning, compaction, or HA

## Transition line to Section 3
“We now have one reliable CDC pipeline. The next question is how to provision, observe, and repeat this safely at platform scale.”
