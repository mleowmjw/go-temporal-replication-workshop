# IDEAS

## Objective

Ideas for future workshop activities (easy to difficult) on Postgres 18

## Scenario #0 - pgdog for read + write  2 Postgres

## Scenario #1 - QA Replica from Prod Aurora

- Declarative pre-req checks in source + target
- pgstream to transform + clone + anon data from Prod Aurora replica

## Scenario # - Blue Green Guided Manual

- LLM plan for Blue Green for review (advanced:simulation)
- Human approval each step of the way
- Follow step --> https://www.bytebase.com/blog/database-blue-green-deployment/
- Manual 5 mins read only; by exclusive lock; fall to read-only
- Emergency rollback / abandon; looks kill process ..
- Idempotent can continue to end goal
- Contract step MUST be explicit .. as need app ready

## Scenario # - Preview of Changes

- https://github.com/sqldef/sqldef-preview-action
- pgschema-diff analysis
- pgschema plan 

## Scenario # - Complex 1
	
- For example add temporarily nullable column to a large table, deploy new code which starts writing to the new column, in background populate that column for existing rows in batches and finally alter column to be mandatory non-nullable.

## Scenario # - Complex 2

- Another example of non-trivial schema management case is to make schema change after new version rollout completes: simple migration at the start of the container can't do that.

## Scenario # - Complex 3

- If I split a Fullname into FirstName and LastName, a diff will only tell half of the story. In EF Core, you will adjust an Up and a Down generated method to make the change reversible, plus you deal with data transformation there. f I reshape a JSONB column into something more structured, I don’t think it would be able to handle that. 

## Scenario # - Complex 4

- Adding or remove columns or indexes can trigger major database table scans and other problems, especially when partitioning conditions change. r if I drop a column the backwards migration it generates ADD COLUMN … NOT NULL, which is obviously unusable if the table has any data in it already.

## Scenario # - Complex migration Measurement

- Take snapshot of Aurora Prod using clone
- Perform changes; measure how long; using various other combo
- Clean up clones

## Scenario # - Advanced Migration w sqldef, pgschema + pgschema-diff

- Dump out new DB with sqldef
- Evaluate plan for change; highlight dangerous stuff pgschema-diff
- Plan + apply with sqldef
- Plan + apply with pgschema-diff
- Plan + apply with pgschema

## Scenario #z Repartition Dynamically with pgdog

- pgstream to load up snapshot with indexes turned off
- pgstream Replay replica from snapshot; transform by partition
- Turn on reindex in Green

## Scenario #y

- Analyze PG based on PGPedia

## Scenario #x - Open source Blue-Green that works with OpenTofu

- OpenTofu spin up cluster from latest source; optionally trigger snapshot
- pgstream to load up snapshot with indexes turned off
- Replay replica from snapshot
- Turn on reindex in Green
- Switch to read-only for 5 mins to make traffic steady 
- Switch pgdog target after in sync; abandon if not meet criteria and turn off read-only

## Scenario # - LLM Advisory for Schema changes + migraiton

- Try out simple
- Try out complex
- Leverage PGPedia if possible ..

## Scenario #

