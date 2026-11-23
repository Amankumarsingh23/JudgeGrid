package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repair"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	DB         *pgxpool.Pool
	Research   research.Service
	Workspace  workspace.WorkspaceManager
	Validation validation.Service
	Model      ModelClient
}
type Task struct {
	ID            string         `json:"id"`
	Scope         research.Scope `json:"scope"`
	Goal          string         `json:"goal"`
	Target        string         `json:"target,omitempty"`
	Policy        string         `json:"policy"`
	Provider      string         `json:"provider"`
	Research      string         `json:"research,omitempty"`
	Workspace     string         `json:"workspace,omitempty"`
	Baseline      string         `json:"baseline,omitempty"`
	Repair        string         `json:"repair,omitempty"`
	State         string         `json:"state"`
	Generation    int64          `json:"generation"`
	Calls         int            `json:"planner_calls"`
	InputTokens   int            `json:"input_tokens"`
	OutputTokens  int            `json:"output_tokens"`
	EvidenceBytes int            `json:"evidence_bytes"`
	ActiveUS      int64          `json:"active_us"`
	Created       time.Time      `json:"created_at"`
	Deadline      time.Time      `json:"deadline"`
	Report        *Report        `json:"report,omitempty"`
	Digest        string         `json:"report_sha256,omitempty"`
	Events        []Event        `json:"events,omitempty"`
}
type Event struct {
	Generation int64     `json:"generation"`
	Action     string    `json:"action"`
	Receipt    string    `json:"receipt,omitempty"`
	At         time.Time `json:"at"`
}
type Report struct {
	Task             string              `json:"task"`
	Scope            research.Scope      `json:"scope"`
	State            string              `json:"state"`
	Provider         string              `json:"provider"`
	Research         string              `json:"research"`
	Citations        []research.Citation `json:"citations,omitempty"`
	Workspace        string              `json:"workspace,omitempty"`
	Version          string              `json:"version,omitempty"`
	Baseline         string              `json:"baseline,omitempty"`
	BaselineDigest   string              `json:"baseline_sha256,omitempty"`
	Validation       string              `json:"validation,omitempty"`
	ValidationDigest string              `json:"validation_sha256,omitempty"`
	Repair           string              `json:"repair,omitempty"`
	RepairDigest     string              `json:"repair_sha256,omitempty"`
	Profile          string              `json:"profile,omitempty"`
	Outcome          string              `json:"validation_outcome,omitempty"`
	Image            string              `json:"validation_image,omitempty"`
	ActiveUS         int64               `json:"active_us,omitempty"`
	WallUS           int64               `json:"wall_us,omitempty"`
	ApprovalWaitUS   int64               `json:"approval_wait_us,omitempty"`
	PlannerCalls     int                 `json:"planner_calls"`
	InputTokens      int                 `json:"input_tokens"`
	OutputTokens     int                 `json:"output_tokens"`
	Uncertainty      string              `json:"uncertainty"`
}

const active = `('CREATED','RESEARCHING','PLANNING','PREPARING_WORKSPACE','VALIDATING_BASELINE','REPAIRING','WAITING_APPROVAL')`
const elapsedClaim = `greatest(0,coalesce((extract(epoch from (least(clock_timestamp(),lease_until)-claim_since))*1000000)::bigint,0))`
const fields = `id::text,repository::text,index_id,snapshot,analysis,commit_oid,goal,target,policy,provider,coalesce(research::text,''),coalesce(workspace::text,''),coalesce(baseline,''),coalesce(repair::text,''),state,generation,calls,input_tokens,output_tokens,evidence_bytes,active_us,created_at,deadline,report,coalesce(report_sha256,'')`

func digest(v any) string { b, _ := json.Marshal(v); return ingest.Digest(b) }
func scan(row pgx.Row) (r Task, e error) {
	e = row.Scan(&r.ID, &r.Scope.Repository, &r.Scope.Retrieval, &r.Scope.Snapshot, &r.Scope.Analysis, &r.Scope.Commit, &r.Goal, &r.Target, &r.Policy, &r.Provider, &r.Research, &r.Workspace, &r.Baseline, &r.Repair, &r.State, &r.Generation, &r.Calls, &r.InputTokens, &r.OutputTokens, &r.EvidenceBytes, &r.ActiveUS, &r.Created, &r.Deadline, &r.Report, &r.Digest)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrDenied
	}
	if e == nil && r.Report != nil && digest(r.Report) != r.Digest {
		e = ErrPolicy
	}
	return
}
func validID(id string) bool     { x, e := uuid.Parse(id); return e == nil && x.String() == id }
func terminal(state string) bool { return !strings.Contains(active, "'"+state+"'") }

func (s Service) Start(ctx context.Context, owner, id, index, goal, target string) (Task, error) {
	if s.Model == nil {
		return Task{}, ErrModelUnavailable
	}
	provider := s.Model.Identity()
	if !validID(id) || owner == "" || len(provider) == 0 || len(provider) > 128 || len(goal) == 0 || len(goal) > 256 || !utf8.ValidString(goal) || strings.ContainsAny(goal, "\x00\r\n") || len(target) > 512 {
		return Task{}, ErrPolicy
	}
	if target != "" && !repair.AllowedTarget(target) {
		return Task{}, ErrPolicy
	}
	i, m, _, e := s.Research.Readers.Retrieval.Ready(ctx, owner, index)
	if e != nil {
		return Task{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Task{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('task:'||$1,0))`, owner); e != nil {
		return Task{}, e
	}
	r, e := scan(tx.QueryRow(ctx, "SELECT "+fields+" FROM research_tasks WHERE id=$1 AND owner=$2", id, owner))
	if e == nil {
		if r.Goal != goal || r.Target != target || r.Provider != provider || r.Scope.Retrieval != i.ID || r.Scope.Commit != m.Commit {
			return r, ErrDenied
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(e, ErrDenied) {
		return r, e
	}
	var ok bool
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE deadline>clock_timestamp() AND state IN `+active+`)<2 AND count(*)<6
 FROM research_tasks WHERE owner=$1 AND created_at>clock_timestamp()-interval '1 hour'`, owner).Scan(&ok)
	if e != nil {
		return r, e
	}
	if !ok {
		return r, ErrBusy
	}
	r, e = scan(tx.QueryRow(ctx, `INSERT INTO research_tasks(id,owner,repository,index_id,snapshot,analysis,commit_oid,goal,target,policy,provider)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+fields, id, owner, i.Repository, i.ID, i.Snapshot, i.Analysis, m.Commit, goal, target, Policy, provider))
	if e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}
func (s Service) Inspect(ctx context.Context, owner, id string) (Task, error) {
	if owner == "" || !validID(id) {
		return Task{}, ErrDenied
	}
	r, e := scan(s.DB.QueryRow(ctx, "SELECT "+fields+" FROM research_tasks WHERE id=$1 AND owner=$2", id, owner))
	if e != nil {
		return r, e
	}
	rows, e := s.DB.Query(ctx, `SELECT generation,action,receipt,at FROM research_task_events WHERE task=$1 ORDER BY sequence`, id)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	for rows.Next() {
		var item Event
		if e = rows.Scan(&item.Generation, &item.Action, &item.Receipt, &item.At); e != nil {
			return r, e
		}
		r.Events = append(r.Events, item)
	}
	return r, rows.Err()
}

type claim struct {
	owner, id, token string
	generation       int64
}

func (s Service) acquire(ctx context.Context, owner, id string) (claim, error) {
	c := claim{owner: owner, id: id, token: uuid.NewString()}
	e := s.DB.QueryRow(ctx, `UPDATE research_tasks SET claim=$3,generation=generation+1,active_us=active_us+`+elapsedClaim+`,claim_since=clock_timestamp(),
 lease_until=least(deadline,clock_timestamp()+interval '6 minutes',clock_timestamp()+(720000000-active_us-`+elapsedClaim+`)*interval '1 microsecond')
 WHERE id=$1 AND owner=$2 AND deadline>clock_timestamp()+interval '30 seconds' AND active_us+`+elapsedClaim+`<690000000
 AND generation<32 AND state IN `+active+` AND (claim IS NULL OR lease_until<=clock_timestamp()) RETURNING generation`, id, owner, c.token).Scan(&c.generation)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrBusy
	}
	return c, e
}
func (s Service) fence(ctx context.Context, c claim, fn func(pgx.Tx) error) error {
	stage, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	tx, e := s.DB.Begin(stage)
	if e != nil {
		return e
	}
	defer tx.Rollback(stage)
	var ok bool
	e = tx.QueryRow(stage, `SELECT true FROM research_tasks WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4 AND state IN `+active+`
 AND lease_until>clock_timestamp()+interval '30 seconds' AND deadline>clock_timestamp() FOR UPDATE`, c.id, c.owner, c.token, c.generation).Scan(&ok)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	}
	if e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(stage)
}
func (s Service) parent(c claim) workspace.Authority {
	return func(ctx context.Context, f func() error) error {
		return s.fence(ctx, c, func(pgx.Tx) error { return f() })
	}
}
func (s Service) release(ctx context.Context, c claim) {
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = s.DB.Exec(clean, `UPDATE research_tasks SET active_us=active_us+`+elapsedClaim+`,claim=NULL,claim_since=NULL,lease_until=NULL WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4`, c.id, c.owner, c.token, c.generation)
}
func addEvent(ctx context.Context, tx pgx.Tx, c claim, action, receipt string) error {
	_, e := tx.Exec(ctx, `INSERT INTO research_task_events(task,generation,action,receipt) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.id, c.generation, action, receipt)
	return e
}
func (s Service) move(ctx context.Context, c claim, previous, next, receipt string) error {
	return s.fence(ctx, c, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE research_tasks SET state=$3 WHERE id=$1 AND state=$2`, c.id, previous, next)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrPolicy
		}
		return addEvent(ctx, tx, c, next, receipt)
	})
}
func (s Service) reserve(ctx context.Context, c claim, kind string, limit int) error {
	return s.fence(ctx, c, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO research_task_reservations(task,kind,maximum) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.id, kind, limit)
		return e
	})
}
func (s Service) model(ctx context.Context, c claim, r Task, in Input) (decision, error) {
	if s.Model == nil || s.Model.Identity() != r.Provider {
		return decision{}, ErrModelUnavailable
	}
	if e := s.fence(ctx, c, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE research_tasks SET calls=calls+1 WHERE id=$1 AND calls<5`, c.id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrPolicy
		}
		return addEvent(ctx, tx, c, "model_call_reserved", "")
	}); e != nil {
		return decision{}, e
	}
	in.Schema = Schema
	in.CallsRemaining = max(0, 4-r.Calls)
	in.InputTokensRemaining = 8192 - r.InputTokens
	in.OutputTokensRemaining = 4096 - r.OutputTokens
	bytesIn, _ := json.Marshal(in)
	if len(bytesIn) > 32768 {
		return decision{}, ErrPolicy
	}
	stage, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	value, e := s.Model.Decide(stage, in)
	if e != nil {
		return decision{}, e
	}
	if stage.Err() != nil {
		return decision{}, stage.Err()
	}
	if value.Usage.InputTokens < 0 || value.Usage.OutputTokens < 0 || value.Usage.InputTokens > in.InputTokensRemaining || value.Usage.OutputTokens > in.OutputTokensRemaining {
		return decision{}, ErrPolicy
	}
	e = s.fence(ctx, c, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE research_tasks SET input_tokens=input_tokens+$2,output_tokens=output_tokens+$3,evidence_bytes=evidence_bytes+$4
    WHERE id=$1 AND input_tokens+$2<=8192 AND output_tokens+$3<=4096 AND evidence_bytes+$4<=65536`, c.id, value.Usage.InputTokens, value.Usage.OutputTokens, len(bytesIn))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrPolicy
		}
		return addEvent(ctx, tx, c, "model_response", ingest.Digest(value.JSON))
	})
	if e != nil {
		return decision{}, e
	}
	d, e := decode(value.JSON)
	if e != nil {
		return d, e
	}
	allowed := false
	for _, a := range in.Allowed {
		if d.Action == a {
			allowed = true
		}
	}
	if !allowed {
		return d, ErrPolicy
	}
	e = s.fence(ctx, c, func(tx pgx.Tx) error { return addEvent(ctx, tx, c, d.Action, ingest.Digest(value.JSON)) })
	return d, e
}

type patchClient struct {
	service Service
	task    Task
	claim   claim
}

func (p patchClient) Identity() string { return p.task.Provider + "/patch" }
func (p patchClient) Propose(ctx context.Context, d repair.Diagnosis) (repair.Decision, error) {
	r, e := p.service.Inspect(ctx, p.claim.owner, p.claim.id)
	if e != nil {
		return repair.Decision{}, e
	}
	next, e := p.service.model(ctx, p.claim, r, Input{UserGoal: r.Goal, State: r.State, Version: d.Version, Repair: &d, Allowed: []string{"PROPOSE_PATCH", "ABSTAIN"}})
	if e != nil {
		return repair.Decision{}, e
	}
	if next.Action == "ABSTAIN" {
		return repair.Decision{Stop: "insufficient_evidence"}, nil
	}
	return repair.Decision{Patch: next.Patch}, nil
}

func (s Service) Resume(ctx context.Context, owner, id string) (r Task, err error) {
	start := time.Now()
	defer func() { telemetry.Observe("task.resume", telemetry.Class(err), "", time.Since(start).Seconds()) }()
	return s.resume(ctx, owner, id, nil)
}
