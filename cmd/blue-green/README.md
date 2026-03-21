# Blue-Green Database Deployment — Hands-On Workshop

**Time:** ~20 minutes &nbsp;|&nbsp; **Prerequisite:** Docker Desktop

---

## The idea (60 seconds)

Your production `customers` table has a `full_name` column. You need to rename it to `display_name` and add a `phone` column — without any downtime and without breaking the old app while the new app is deploying.

The trick is **expand then contract**:

1. **Expand** — add the new columns. Both old and new app work at the same time.
2. **Contract** — only *after* the new app is proven healthy, drop the old column.

Between expand and contract you have a safe window where you can roll back at any time. Today you'll drive that entire lifecycle through a Temporal workflow, approving each gate yourself.

---

## 1. Start everything

```bash
# From the repository root:
docker compose -f cmd/blue-green/compose.workshop.yml up
```

Docker will pull three images (`postgres:18`, `temporalio/temporal-cli`, your workshop app) and start them. First run takes ~2 minutes; subsequent runs start in seconds.

**Wait until you see:**
```
app  | time=... msg="HTTP server listening" addr=:8083
app  | time=... msg="temporal worker started" task_queue=blue-green-deployments
```

**Verify everything is up** (new terminal, same directory):

```bash
# App health
curl http://localhost:8083/healthz
# → {"status":"ok"}

# Temporal UI — open in your browser
open http://localhost:8233     # Mac
xdg-open http://localhost:8233 # Linux
# Windows: open http://localhost:8233 manually
```

---

## 2. See the original schema

```bash
docker compose -f cmd/blue-green/compose.workshop.yml \
  exec postgres psql -U postgres appdb \
  -c "\d inventory.customers"
```

You should see `full_name` but **no** `display_name` or `phone`. That's the before state.

```bash
docker compose -f cmd/blue-green/compose.workshop.yml \
  exec postgres psql -U postgres appdb \
  -c "SELECT id, email, full_name FROM inventory.customers"
```

Three rows: Alice Tan, Bob Lee, Carol Wang — all have `full_name`.

---

## 3. Submit the migration plan

```bash
curl -s -X POST http://localhost:8083/v1/deployments \
  -H "Content-Type: application/json" \
  -d '{
    "ID": "rename-v1",
    "Description": "Add display_name and phone; drop full_name later",
    "ExpandSQL": [
      "ALTER TABLE inventory.customers ADD COLUMN display_name TEXT",
      "UPDATE inventory.customers SET display_name = full_name WHERE display_name IS NULL",
      "ALTER TABLE inventory.customers ADD COLUMN phone TEXT"
    ],
    "ContractSQL": [
      "ALTER TABLE inventory.customers DROP COLUMN full_name"
    ],
    "RollbackSQL": [
      "ALTER TABLE inventory.customers DROP COLUMN IF EXISTS display_name",
      "ALTER TABLE inventory.customers DROP COLUMN IF EXISTS phone"
    ],
    "VerifyQueries": [{
      "Name": "backfill_complete",
      "SQL": "SELECT count(*) FROM inventory.customers WHERE display_name IS NULL",
      "WantCount": 0
    }]
  }'
```

You should get back:
```json
{"deployment_id":"rename-v1","workflow_id":"bg-deploy-rename-v1","status":"pending"}
```

**Check Temporal UI:** refresh [http://localhost:8233](http://localhost:8233) — you'll see a running workflow named `bg-deploy-rename-v1`. It's waiting at the `plan_review` gate.

---

## 4. Approve: run the expand

```bash
curl -s -X POST http://localhost:8083/v1/deployments/rename-v1/approve \
  -H "Content-Type: application/json" \
  -d '{"Note":"Plan looks good"}'
```

The workflow runs the expand SQL. Check the database:

```bash
docker compose -f cmd/blue-green/compose.workshop.yml \
  exec postgres psql -U postgres appdb \
  -c "SELECT id, full_name, display_name, phone FROM inventory.customers"
```

`display_name` is populated from `full_name`. `phone` is NULL. **Both columns coexist** — this is the safe window.

Check the workflow phase:
```bash
curl -s http://localhost:8083/v1/deployments/rename-v1 | grep -o '"Phase":"[^"]*"'
# → "Phase":"expand_verify"
```

The workflow ran an app-compatibility check and confirmed: **old app passes, new app passes**. It's waiting for your review.

---

## 5. Approve: proceed to cutover

```bash
curl -s -X POST http://localhost:8083/v1/deployments/rename-v1/approve \
  -H "Content-Type: application/json" \
  -d '{"Note":"Compat check passed"}'
```

The workflow briefly sets the database read-only, switches traffic from old→new app, then releases the lock. This whole window is capped at 5 minutes; in production you'd complete it in seconds.

```bash
curl -s http://localhost:8083/v1/deployments/rename-v1 | grep -o '"Phase":"[^"]*"'
# → "Phase":"monitoring"
```

---

## 6. Approve: new app is healthy, prepare for contract

In production you'd wait here — hours or days — watching error rates. Today we'll proceed immediately.

```bash
curl -s -X POST http://localhost:8083/v1/deployments/rename-v1/approve \
  -H "Content-Type: application/json" \
  -d '{"Note":"Metrics look good"}'
```

```bash
curl -s http://localhost:8083/v1/deployments/rename-v1 | grep -o '"Phase":"[^"]*"'
# → "Phase":"contract_wait"
```

The workflow ran a final compat check (green passes, ready to contract) and stopped. It will not go further without your explicit say-so.

---

## 7. Approve: drop the old column (point of no return)

```bash
curl -s -X POST http://localhost:8083/v1/deployments/rename-v1/approve \
  -H "Content-Type: application/json" \
  -d '{"Note":"All clear — drop full_name"}'
```

```bash
curl -s http://localhost:8083/v1/deployments/rename-v1 | grep -o '"Phase":"[^"]*"'
# → "Phase":"complete"
```

Verify `full_name` is gone:

```bash
docker compose -f cmd/blue-green/compose.workshop.yml \
  exec postgres psql -U postgres appdb \
  -c "\d inventory.customers"
```

`full_name` has been dropped. `display_name` and `phone` remain. **Migration complete — zero downtime.**

---

## Bonus: trigger an emergency rollback

Reset the database, then run through again and send a `rollback` signal during monitoring instead of approving.

```bash
# Reset to original schema
docker compose -f cmd/blue-green/compose.workshop.yml restart postgres
# Wait ~5 seconds for postgres to come back, then:

# Submit a new deployment
curl -s -X POST http://localhost:8083/v1/deployments \
  -H "Content-Type: application/json" \
  -d '{
    "ID": "rename-v2",
    "Description": "Same migration — we will roll this one back",
    "ExpandSQL": [
      "ALTER TABLE inventory.customers ADD COLUMN display_name TEXT",
      "UPDATE inventory.customers SET display_name = full_name WHERE display_name IS NULL",
      "ALTER TABLE inventory.customers ADD COLUMN phone TEXT"
    ],
    "ContractSQL": ["ALTER TABLE inventory.customers DROP COLUMN full_name"],
    "RollbackSQL": [
      "ALTER TABLE inventory.customers DROP COLUMN IF EXISTS display_name",
      "ALTER TABLE inventory.customers DROP COLUMN IF EXISTS phone"
    ],
    "VerifyQueries": [{"Name":"check","SQL":"SELECT count(*) FROM inventory.customers WHERE display_name IS NULL","WantCount":0}]
  }'

# Approve plan_review → expand runs
curl -s -X POST http://localhost:8083/v1/deployments/rename-v2/approve \
  -H "Content-Type: application/json" -d '{}'

# Approve expand_verify → cutover
curl -s -X POST http://localhost:8083/v1/deployments/rename-v2/approve \
  -H "Content-Type: application/json" -d '{}'

# Now in monitoring — something went wrong! ROLLBACK instead of approve:
curl -s -X POST http://localhost:8083/v1/deployments/rename-v2/rollback \
  -H "Content-Type: application/json" \
  -d '{"Reason":"Error rate spiked after cutover"}'
```

```bash
curl -s http://localhost:8083/v1/deployments/rename-v2 | grep -o '"Phase":"[^"]*"'
# → "Phase":"rolled_back"

docker compose -f cmd/blue-green/compose.workshop.yml \
  exec postgres psql -U postgres appdb \
  -c "\d inventory.customers"
# display_name and phone are gone — back to original schema
```

---

## Cleanup

```bash
# Ctrl+C in the terminal running docker compose, then:
docker compose -f cmd/blue-green/compose.workshop.yml down -v
```

This removes all containers and the data volume. Your system is back to its original state.

---

## Optional: run the test suite

The tests prove every concept (expand, contract, rollback, compat checks) using in-memory fakes — no database needed.

```bash
docker compose -f cmd/blue-green/compose.workshop.yml run --rm test
```

Look for the `TestAppCompat_*` tests — they show exactly which queries fail and pass at each migration phase.

---

## Troubleshooting

**`app` keeps restarting / "failed to dial temporal"**
Temporal takes a few seconds to start. Docker's healthcheck handles this, but if you see startup errors just wait — the app will retry and connect once Temporal is ready.

**Port conflict (8083, 8233, or 7233)**
Another process is using that port. Find and stop it:
```bash
# Mac/Linux
lsof -i :8083
# Windows PowerShell
netstat -ano | findstr :8083
```

**`docker compose` not found**
You need Docker Desktop v2.x or newer (it includes the `compose` plugin). Older installs used `docker-compose` (hyphen). Try: `docker-compose -f ...`

**Want to start fresh mid-workshop**
```bash
docker compose -f cmd/blue-green/compose.workshop.yml down -v
docker compose -f cmd/blue-green/compose.workshop.yml up
```
This wipes the database and Temporal history and starts clean.
