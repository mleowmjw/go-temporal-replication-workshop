package bluegreen

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/starfederation/datastar-go/datastar"
)

//go:embed templates
var templateFS embed.FS

// uiDemoPlan is the pre-configured workshop migration plan displayed in the dashboard.
var uiDemoPlan = MigrationPlan{
	ID:          "rename-v1",
	Description: "Add display_name and phone; drop full_name later",
	ExpandSQL: []string{
		"ALTER TABLE inventory.customers ADD COLUMN IF NOT EXISTS display_name TEXT",
		"ALTER TABLE inventory.customers ADD COLUMN IF NOT EXISTS phone TEXT",
		"UPDATE inventory.customers SET display_name = full_name WHERE display_name IS NULL",
	},
	ContractSQL: []string{
		"ALTER TABLE inventory.customers DROP COLUMN IF EXISTS full_name CASCADE",
		"ALTER TABLE inventory.customers ADD COLUMN IF NOT EXISTS search_key TEXT GENERATED ALWAYS AS (lower(display_name) || ' ' || lower(email)) STORED",
	},
	RollbackSQL: []string{
		"ALTER TABLE inventory.customers DROP COLUMN IF EXISTS display_name",
		"ALTER TABLE inventory.customers DROP COLUMN IF EXISTS phone",
	},
	VerifyQueries: []VerifyQuery{{
		Name:      "backfill_complete",
		SQL:       "SELECT count(*) FROM inventory.customers WHERE display_name IS NULL",
		WantCount: 0,
	}},
}

// columnInfo is the display model for a single DB column.
type columnInfo struct {
	Name           string
	DataType       string
	IsNullable     string
	Default        string
	GenerationExpr string
	IsNew          bool // true for columns added by the expand phase
}

// queryCompatResult is the display model for one app-query compat check.
type queryCompatResult struct {
	Name string
	SQL  string
	Pass bool
	Err  string
}

// stepInfo is one node in the visual phase stepper.
type stepInfo struct {
	Label string
	Phase Phase
	Class string // "done", "current", "rollback", "failed", or ""
	Done  bool
}

// debugEntry is one HTTP request/response shown in the dev debug overlay.
type debugEntry struct {
	Time   string
	Curl   string
	Status string // "200 OK", "201 Created", "err: …"
	Body   string // truncated response body
}

const maxDebugEntries = 30

// dashboardData is the value passed to every panel template render.
type dashboardData struct {
	OpsState    *DatabaseOpsState
	Deployment  *DeploymentStatus
	Schema      []columnInfo
	HasNewCols  bool
	RowHeaders  []string
	Rows        [][]string
	BlueCompat  []queryCompatResult
	GreenCompat []queryCompatResult
	DBConnected bool
	Steps       []stepInfo
	Flash       string // transient success/error message shown once
	FlashType   string // "ok" or "err"
	DebugLog    []debugEntry
}

// UIHandler serves the interactive workshop dashboard.
type UIHandler struct {
	pool   *pgxpool.Pool // nil in BG_MODE=test
	client WorkflowClient
	app    AppSimulator
	tmpl   *template.Template
	log    *slog.Logger

	debugMu  sync.Mutex
	debugLog []debugEntry
}

// NewUIHandler creates a UIHandler. pool may be nil when running without a real DB.
func NewUIHandler(pool *pgxpool.Pool, client WorkflowClient, app AppSimulator, log *slog.Logger) (*UIHandler, error) {
	log.Info("parsing UI templates from embedded FS")
	tmpl, err := template.New("").Funcs(uiTemplateFuncs()).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Error("template parse error", "error", err)
		return nil, fmt.Errorf("parse UI templates: %w", err)
	}
	names := make([]string, 0, len(tmpl.Templates()))
	for _, t := range tmpl.Templates() {
		names = append(names, t.Name())
	}
	log.Info("UI templates parsed OK", "templates", names)
	return &UIHandler{
		pool:   pool,
		client: client,
		app:    app,
		tmpl:   tmpl,
		log:    log,
	}, nil
}

// RegisterUIRoutes mounts the dashboard routes on mux.
func (h *UIHandler) RegisterUIRoutes(mux *http.ServeMux) {
	h.log.Info("registering UI routes")
	mux.HandleFunc("GET /{$}", h.serveIndex)
	mux.HandleFunc("GET /ui", h.serveIndex)
	mux.HandleFunc("GET /ui/{$}", h.serveIndex)
	mux.HandleFunc("GET /ui/state", h.serveState)
	mux.HandleFunc("POST /ui/deploy", h.deploy)
	mux.HandleFunc("POST /ui/approve", h.approve)
	mux.HandleFunc("POST /ui/rollback", h.rollback)

	// Datastar debug sandbox — isolated page with dummy endpoints.
	mux.HandleFunc("GET /ui/debug", h.serveDebugPage)
	mux.HandleFunc("GET /ui/debug/", h.serveDebugPage)
	mux.HandleFunc("POST /ui/debug/echo", h.debugEcho)
	mux.HandleFunc("GET /ui/debug/ping", h.debugPing)
	mux.HandleFunc("POST /ui/debug/mirror", h.debugMirror)
	mux.HandleFunc("POST /ui/debug/clear", h.debugClear)

	h.log.Info("UI routes registered", "routes", []string{
		"GET /", "GET /ui", "GET /ui/", "GET /ui/state",
		"POST /ui/deploy", "POST /ui/approve", "POST /ui/rollback",
		"GET /ui/debug", "POST /ui/debug/echo", "GET /ui/debug/ping",
		"POST /ui/debug/mirror", "POST /ui/debug/clear",
	})
}

// ── HTTP handlers ──────────────────────────────────────────────────────────────

func (h *UIHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	h.log.Info("UI: serving dashboard index", "path", r.URL.Path)
	data := h.collectData(r.Context())
	data.DebugLog = h.getDebugLog()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
		h.log.Error("render index", "error", err)
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *UIHandler) serveState(w http.ResponseWriter, r *http.Request) {
	data := h.collectData(r.Context())
	data.DebugLog = h.getDebugLog()
	sse := datastar.NewSSE(w, r)
	h.patchAllPanels(sse, data)
}

func (h *UIHandler) deploy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	curlCmd := fmt.Sprintf(`curl -s -X POST http://localhost:8083/v1/deployments -H "Content-Type: application/json" -d '%s'`, jsonCompact(uiDemoPlan))

	wfID, err := h.client.StartDeployment(ctx, uiDemoPlan)

	// Brief pause so the workflow state is queryable before we re-render.
	time.Sleep(400 * time.Millisecond)
	data := h.collectData(ctx)

	switch {
	case err == nil:
		data.Flash = "Deployment started! Workflow is now in Plan Review."
		data.FlashType = "ok"
		h.recordDebug(curlCmd, "201 Created", fmt.Sprintf(`{"deployment_id":"%s","workflow_id":"%s","status":"pending"}`, uiDemoPlan.ID, wfID))
	case errors.Is(err, ErrDeploymentAlreadyExists):
		data.Flash = "A deployment with this plan ID already exists."
		data.FlashType = "err"
		h.recordDebug(curlCmd, "409 Conflict", `{"error":"deployment already exists: `+uiDemoPlan.ID+`"}`)
	case errors.Is(err, ErrSchemaCurrentlyLocked):
		data.Flash = "Another deployment holds the schema lock. Wait for it to finish."
		data.FlashType = "err"
		h.recordDebug(curlCmd, "409 Conflict", `{"error":"schema lock held by another deployment"}`)
	default:
		h.log.Error("start deployment via UI", "error", err)
		data.Flash = "Failed to start deployment: " + err.Error()
		data.FlashType = "err"
		h.recordDebug(curlCmd, "500 Error", err.Error())
	}

	data.DebugLog = h.getDebugLog()
	sse := datastar.NewSSE(w, r)
	h.patchAllPanels(sse, data)
}

func (h *UIHandler) approve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	planID := h.activePlanID(ctx)
	if planID != "" {
		curlCmd := fmt.Sprintf(`curl -s -X POST http://localhost:8083/v1/deployments/%s/approve -H "Content-Type: application/json" -d '{"Note":"workshop dashboard approval"}'`, planID)
		if err := h.client.ApproveDeployment(ctx, planID, ApprovalPayload{Note: "workshop dashboard approval"}); err != nil {
			h.log.Warn("approve via UI failed", "planID", planID, "error", err)
			h.recordDebug(curlCmd, "500 Error", err.Error())
		} else {
			h.recordDebug(curlCmd, "202 Accepted", `{"status":"approved"}`)
		}
	}
	time.Sleep(400 * time.Millisecond)
	data := h.collectData(ctx)
	data.DebugLog = h.getDebugLog()
	sse := datastar.NewSSE(w, r)
	h.patchAllPanels(sse, data)
}

func (h *UIHandler) rollback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var signals struct {
		RollbackReason string `json:"rollbackReason"`
	}
	_ = datastar.ReadSignals(r, &signals)
	reason := signals.RollbackReason
	if reason == "" {
		reason = "rollback requested from dashboard"
	}
	planID := h.activePlanID(ctx)
	if planID != "" {
		curlCmd := fmt.Sprintf(`curl -s -X POST http://localhost:8083/v1/deployments/%s/rollback -H "Content-Type: application/json" -d '{"Reason":"%s"}'`, planID, reason)
		if err := h.client.RollbackDeployment(ctx, planID, RollbackPayload{Reason: reason}); err != nil {
			h.log.Warn("rollback via UI failed", "planID", planID, "error", err)
			h.recordDebug(curlCmd, "500 Error", err.Error())
		} else {
			h.recordDebug(curlCmd, "202 Accepted", `{"status":"rollback_requested"}`)
		}
	}
	time.Sleep(400 * time.Millisecond)
	data := h.collectData(ctx)
	data.DebugLog = h.getDebugLog()
	sse := datastar.NewSSE(w, r)
	h.patchAllPanels(sse, data)
}

// ── Debug sandbox handlers ──────────────────────────────────────────────────────

func (h *UIHandler) serveDebugPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "debug-page.html", nil); err != nil {
		h.log.Error("render debug page", "error", err)
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *UIHandler) debugEcho(w http.ResponseWriter, r *http.Request) {
	h.recordDebug(
		`curl -s -X POST http://localhost:8083/ui/debug/echo`,
		"200 OK",
		`{"msg":"Echo from server","method":"POST","time":"`+time.Now().Format("15:04:05")+`"}`,
	)
	sse := datastar.NewSSE(w, r)
	_ = sse.PatchElements(fmt.Sprintf(
		`<div id="echo-result" class="result-box"><span class="pass">POST OK</span> at %s — server received the request and replied via SSE PatchElements.</div>`,
		time.Now().Format("15:04:05"),
	))
	h.patchDebugLog(sse)
}

func (h *UIHandler) debugPing(w http.ResponseWriter, r *http.Request) {
	h.recordDebug(
		`curl -s http://localhost:8083/ui/debug/ping`,
		"200 OK",
		`{"pong":true,"time":"`+time.Now().Format("15:04:05")+`"}`,
	)
	sse := datastar.NewSSE(w, r)
	_ = sse.PatchElements(fmt.Sprintf(
		`<div id="ping-result" class="result-box"><span class="pass">GET pong!</span> at %s — server responded to GET with SSE.</div>`,
		time.Now().Format("15:04:05"),
	))
	h.patchDebugLog(sse)
}

func (h *UIHandler) debugMirror(w http.ResponseWriter, r *http.Request) {
	var signals json.RawMessage
	if err := datastar.ReadSignals(r, &signals); err != nil {
		signals = json.RawMessage(`{"error":"` + err.Error() + `"}`)
	}
	compact := string(signals)
	if len(compact) > 400 {
		compact = compact[:400] + "…"
	}
	h.recordDebug(
		`curl -s -X POST http://localhost:8083/ui/debug/mirror -d '<signals>'`,
		"200 OK",
		compact,
	)
	sse := datastar.NewSSE(w, r)
	_ = sse.PatchElements(fmt.Sprintf(
		`<div id="mirror-result" class="result-box"><span class="pass">Received signals</span><br><code style="word-break:break-all;font-size:0.75rem;">%s</code></div>`,
		template.HTMLEscapeString(compact),
	))
	h.patchDebugLog(sse)
}

func (h *UIHandler) debugClear(w http.ResponseWriter, r *http.Request) {
	h.debugMu.Lock()
	h.debugLog = nil
	h.debugMu.Unlock()

	sse := datastar.NewSSE(w, r)
	_ = sse.PatchElements(`<div id="sandbox-debug-body" class="debug-panel-body"><div class="debug-entry" style="color:#8b949e;font-style:italic;">Log cleared.</div></div>`)
}

// patchDebugLog renders the sandbox debug panel body via SSE.
func (h *UIHandler) patchDebugLog(sse *datastar.ServerSentEventGenerator) {
	entries := h.getDebugLog()
	var buf bytes.Buffer
	buf.WriteString(`<div id="sandbox-debug-body" class="debug-panel-body">`)
	if len(entries) == 0 {
		buf.WriteString(`<div class="debug-entry" style="color:#8b949e;font-style:italic;">No requests yet.</div>`)
	} else {
		for _, e := range entries {
			statusClass := "ok"
			if len(e.Status) > 0 && e.Status[0] != '2' {
				statusClass = "err"
			}
			fmt.Fprintf(&buf,
				`<div class="debug-entry"><div><span class="debug-time">%s</span><span class="debug-status %s">%s</span></div><div class="debug-curl">$ %s</div>`,
				template.HTMLEscapeString(e.Time),
				statusClass,
				template.HTMLEscapeString(e.Status),
				template.HTMLEscapeString(e.Curl),
			)
			if e.Body != "" {
				fmt.Fprintf(&buf, `<div class="debug-body">→ %s</div>`, template.HTMLEscapeString(e.Body))
			}
			buf.WriteString(`</div>`)
		}
	}
	buf.WriteString(`</div>`)
	_ = sse.PatchElements(buf.String())
}

// activePlanID returns the plan ID of the currently active deployment, or "".
func (h *UIHandler) activePlanID(ctx context.Context) string {
	state, err := h.client.GetDatabaseOpsState(ctx)
	if err != nil || state.ActiveDeployment == nil {
		return ""
	}
	return state.ActiveDeployment.PlanID
}

// recordDebug appends a debug entry visible in the dev overlay.
func (h *UIHandler) recordDebug(curl, status, body string) {
	h.debugMu.Lock()
	defer h.debugMu.Unlock()
	entry := debugEntry{
		Time:   time.Now().Format("15:04:05"),
		Curl:   curl,
		Status: status,
		Body:   body,
	}
	if len(entry.Body) > 300 {
		entry.Body = entry.Body[:300] + "…"
	}
	h.debugLog = append(h.debugLog, entry)
	if len(h.debugLog) > maxDebugEntries {
		h.debugLog = h.debugLog[len(h.debugLog)-maxDebugEntries:]
	}
}

func (h *UIHandler) getDebugLog() []debugEntry {
	h.debugMu.Lock()
	defer h.debugMu.Unlock()
	out := make([]debugEntry, len(h.debugLog))
	copy(out, h.debugLog)
	return out
}

func jsonCompact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// patchAllPanels renders every panel template and sends each as a Datastar PatchElements event.
func (h *UIHandler) patchAllPanels(sse *datastar.ServerSentEventGenerator, data dashboardData) {
	for _, name := range []string{
		"flash-banner",
		"phase-stepper",
		"control-bar",
		"schema-panel",
		"data-panel",
		"compat-panel",
		"lock-panel",
		"history-panel",
		"debug-panel",
	} {
		var buf bytes.Buffer
		if err := h.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			h.log.Error("render panel", "name", name, "error", err)
			continue
		}
		_ = sse.PatchElements(buf.String())
	}
}

// ── Data collection ────────────────────────────────────────────────────────────

func (h *UIHandler) collectData(ctx context.Context) dashboardData {
	data := dashboardData{DBConnected: h.pool != nil}

	// Coordinator state.
	if opsState, err := h.client.GetDatabaseOpsState(ctx); err == nil {
		data.OpsState = &opsState
	}

	// Active deployment status (if coordinator knows about one).
	if data.OpsState != nil && data.OpsState.ActiveDeployment != nil {
		if status, err := h.client.GetDeploymentStatus(ctx, data.OpsState.ActiveDeployment.PlanID); err == nil {
			data.Deployment = &status
		}
	}

	// DB queries (skipped when pool is nil, e.g., BG_MODE=test).
	if h.pool != nil {
		data.Schema = h.querySchema(ctx)
		for _, c := range data.Schema {
			if c.IsNew {
				data.HasNewCols = true
				break
			}
		}
		data.RowHeaders, data.Rows = h.queryRows(ctx, data.Schema)
		data.BlueCompat = h.checkCompat(ctx, h.app.BlueQueries())
		data.GreenCompat = h.checkCompat(ctx, h.app.GreenQueries())
	}

	data.Steps = buildSteps(data.Deployment)
	return data
}

// ── DB helpers ─────────────────────────────────────────────────────────────────

// initialColumns is the baseline schema before any migration runs.
// Columns not in this set are highlighted as "new" in the schema panel.
var initialColumns = map[string]bool{
	"id": true, "email": true, "full_name": true,
	"status": true, "created_at": true, "search_key": true,
}

func (h *UIHandler) querySchema(ctx context.Context) []columnInfo {
	rows, err := h.pool.Query(ctx, `
		SELECT column_name, data_type, is_nullable,
		       COALESCE(column_default, ''),
		       COALESCE(generation_expression, '')
		FROM information_schema.columns
		WHERE table_schema = 'inventory' AND table_name = 'customers'
		ORDER BY ordinal_position`)
	if err != nil {
		h.log.Warn("schema query failed", "error", err)
		return nil
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.Name, &c.DataType, &c.IsNullable, &c.Default, &c.GenerationExpr); err != nil {
			continue
		}
		c.IsNew = !initialColumns[c.Name]
		cols = append(cols, c)
	}
	return cols
}

func (h *UIHandler) queryRows(ctx context.Context, schema []columnInfo) (headers []string, rows [][]string) {
	if len(schema) == 0 {
		return nil, nil
	}
	for _, c := range schema {
		headers = append(headers, c.Name)
	}

	pgRows, err := h.pool.Query(ctx, `SELECT * FROM inventory.customers ORDER BY id LIMIT 5`)
	if err != nil {
		h.log.Warn("rows query failed", "error", err)
		return headers, nil
	}
	defer pgRows.Close()

	// Build an index of fieldName → column position in the result set.
	fieldIdx := make(map[string]int, len(pgRows.FieldDescriptions()))
	for i, fd := range pgRows.FieldDescriptions() {
		fieldIdx[string(fd.Name)] = i
	}

	for pgRows.Next() {
		vals, err := pgRows.Values()
		if err != nil {
			continue
		}
		row := make([]string, len(headers))
		for hi, colName := range headers {
			if idx, ok := fieldIdx[colName]; ok && idx < len(vals) {
				if vals[idx] != nil {
					row[hi] = fmt.Sprintf("%v", vals[idx])
				} else {
					row[hi] = "NULL"
				}
			}
		}
		rows = append(rows, row)
	}
	return headers, rows
}

func (h *UIHandler) checkCompat(ctx context.Context, queries []AppQuery) []queryCompatResult {
	migrator := NewPgDatabaseMigrator(h.pool)
	results := make([]queryCompatResult, len(queries))
	for i, q := range queries {
		err := migrator.ValidateQuery(ctx, q.SQL)
		results[i] = queryCompatResult{Name: q.Name, SQL: q.SQL, Pass: err == nil}
		if err != nil {
			results[i].Err = err.Error()
		}
	}
	return results
}

// ── Phase stepper ──────────────────────────────────────────────────────────────

// planPhases, expandPhases, contractPhases split the lifecycle into three visual groups.
var planPhases = []Phase{PhasePlanReview}
var expandPhases = []Phase{PhaseExpanding, PhaseExpandVerify, PhaseCutover}
var contractPhases = []Phase{PhaseMonitoring, PhaseContractWait, PhaseContracting, PhaseComplete}

var happyPathPhases = func() []Phase {
	all := make([]Phase, 0, len(planPhases)+len(expandPhases)+len(contractPhases))
	all = append(all, planPhases...)
	all = append(all, expandPhases...)
	all = append(all, contractPhases...)
	return all
}()

var phaseOrder = func() map[Phase]int {
	m := make(map[Phase]int, len(happyPathPhases))
	for i, p := range happyPathPhases {
		m[p] = i
	}
	return m
}()

var phaseLabels = map[Phase]string{
	PhasePlanReview:   "Plan Review",
	PhaseExpanding:    "Expanding",
	PhaseExpandVerify: "Verify",
	PhaseCutover:      "Cutover",
	PhaseMonitoring:   "Monitoring",
	PhaseContractWait: "Approve",
	PhaseContracting:  "Contracting",
	PhaseComplete:     "Complete",
}

// PlanStepCount and ExpandStepEnd are exposed to templates for splitting the stepper.
var PlanStepCount = len(planPhases)
var ExpandStepEnd = len(planPhases) + len(expandPhases)

func buildSteps(deployment *DeploymentStatus) []stepInfo {
	steps := make([]stepInfo, len(happyPathPhases))
	for i, p := range happyPathPhases {
		steps[i] = stepInfo{Label: phaseLabels[p], Phase: p}
	}
	if deployment == nil {
		return steps
	}

	currentPhase := deployment.Phase
	currentIdx, inHappyPath := phaseOrder[currentPhase]

	for i, p := range happyPathPhases {
		switch {
		case currentPhase == PhaseRolledBack:
			if inHappyPath && i < currentIdx {
				steps[i].Class = "done"
				steps[i].Done = true
			} else if p == PhasePlanReview && currentIdx == 0 {
				steps[i].Class = "rollback"
			}
		case currentPhase == PhaseFailed:
			if inHappyPath && i < currentIdx {
				steps[i].Class = "done"
				steps[i].Done = true
			} else if inHappyPath && i == currentIdx {
				steps[i].Class = "failed"
			}
		case inHappyPath && i < currentIdx:
			steps[i].Class = "done"
			steps[i].Done = true
		case inHappyPath && i == currentIdx:
			steps[i].Class = "current"
		}
	}
	return steps
}

// ── Template functions ─────────────────────────────────────────────────────────

func uiTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"add":            func(a, b int) int { return a + b },
		"planStepCount":  func() int { return PlanStepCount },
		"expandStepEnd":  func() int { return ExpandStepEnd },
		"hasPrefix":      func(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix },
		"isActivePhase": func(p Phase) bool {
			switch p {
			case PhaseComplete, PhaseRolledBack, PhaseFailed, "":
				return false
			}
			return true
		},

		"allPass": func(results []queryCompatResult) bool {
			for _, r := range results {
				if !r.Pass {
					return false
				}
			}
			return true
		},

		"isApprovalPhase": func(p Phase) bool {
			switch p {
			case PhasePlanReview, PhaseExpandVerify, PhaseMonitoring, PhaseContractWait:
				return true
			}
			return false
		},

		"isTerminalPhase": func(p Phase) bool {
			switch p {
			case PhaseComplete, PhaseRolledBack, PhaseFailed:
				return true
			}
			return false
		},

		"approveLabel": func(p Phase) string {
			switch p {
			case PhasePlanReview:
				return "Approve Plan → Start Expand"
			case PhaseExpandVerify:
				return "Approve Expand → Cut Over Traffic"
			case PhaseMonitoring:
				return "Approve Monitoring → Wait for Contract"
			case PhaseContractWait:
				return "Drop Old Columns (irreversible!)"
			}
			return "Approve"
		},

		"phaseTitle": func(p Phase) string {
			switch p {
			case PhasePlanReview:
				return "Plan Review — Your approval gate"
			case PhaseExpanding:
				return "Expanding — Running DDL safely"
			case PhaseExpandVerify:
				return "Expand Verified — Your approval gate"
			case PhaseCutover:
				return "Cutover — Switching traffic"
			case PhaseMonitoring:
				return "Monitoring — Green app is live"
			case PhaseContractWait:
				return "Contract Wait — Final approval gate ⚠️"
			case PhaseContracting:
				return "Contracting — Removing old columns"
			case PhaseComplete:
				return "Complete! 🎉"
			case PhaseRolledBack:
				return "Rolled Back ⏪"
			case PhaseFailed:
				return "Failed 💥"
			}
			return string(p)
		},

		"phaseDescription": func(p Phase) string {
			switch p {
			case PhasePlanReview:
				return "Review the ExpandSQL, ContractSQL, and RollbackSQL. " +
					"Expand adds new columns while keeping old ones intact — both blue (v1) and green (v2) apps will work simultaneously. " +
					"Click Approve when ready."
			case PhaseExpanding:
				return "Running ExpandSQL: adding display_name and phone columns, backfilling data from full_name. " +
					"The old full_name column is still present — both app versions are fully compatible."
			case PhaseExpandVerify:
				return "Expand complete! Verify the schema diff and app compatibility panels. " +
					"Both blue and green queries should pass. " +
					"Click Approve to briefly lock the DB and switch traffic to the green app."
			case PhaseCutover:
				return "The database is briefly set to READ-ONLY while traffic switches from blue (v1) to green (v2). " +
					"This typically takes less than 30 seconds."
			case PhaseMonitoring:
				return "Green app (v2) is now live! The old full_name column still exists — you can still roll back at this point. " +
					"Monitor your dashboards and click Approve when confident the new app is healthy."
			case PhaseContractWait:
				return "⚠️  Point of no return! Approving here will DROP the full_name column. " +
					"There is NO rollback after this step. Make sure green app (v2) is fully stable."
			case PhaseContracting:
				return "Running ContractSQL: dropping full_name (with CASCADE), then recreating search_key using display_name. " +
					"The old blue app (v1) is no longer compatible after this."
			case PhaseComplete:
				return "Migration complete! The customers table now uses display_name and phone. " +
					"The old full_name column has been removed. search_key uses the new display_name column."
			case PhaseRolledBack:
				return "The deployment was rolled back. RollbackSQL removed display_name and phone columns. " +
					"The schema is back to its original state with full_name."
			case PhaseFailed:
				return "The deployment encountered an unexpected error. " +
					"Check the Temporal UI (http://localhost:8233) for details."
			}
			return ""
		},

		"phaseExplainClass": func(p Phase) string {
			switch p {
			case PhaseExpandVerify, PhaseMonitoring:
				return "warn"
			case PhaseContractWait:
				return "danger"
			case PhaseCutover, PhaseContracting:
				return "warn"
			case PhaseComplete:
				return "success"
			case PhaseRolledBack:
				return "rolled"
			case PhaseFailed:
				return "danger"
			}
			return ""
		},

		"lockTimeout": func(env Environment) string {
			d := env.LockTimeout()
			h := d.Hours()
			if h >= 1 {
				return fmt.Sprintf("%.0fh", h)
			}
			return fmt.Sprintf("%.0fm", d.Minutes())
		},

		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("2006-01-02 15:04:05")
		},
	}
}
