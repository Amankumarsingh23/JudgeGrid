package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	DB      *pgxpool.Pool
	Readers Readers
	Planner Planner
}

type Run struct {
	ID         string     `json:"id"`
	Scope      Scope      `json:"scope"`
	Goal       string     `json:"goal"`
	Planner    string     `json:"planner"`
	State      string     `json:"state"`
	Generation int64      `json:"generation"`
	Deadline   time.Time  `json:"deadline"`
	Created    time.Time  `json:"created_at"`
	Updated    time.Time  `json:"updated_at"`
	Failure    string     `json:"failure,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Steps      []Step     `json:"steps"`
	Approvals  []Approval `json:"approvals,omitempty"`
}

type Approval struct {
	Ordinal   int       `json:"ordinal"`
	Digest    string    `json:"action_sha256"`
	State     string    `json:"state"`
	Requester string    `json:"requested_by"`
	Decider   string    `json:"decided_by,omitempty"`
	Expires   time.Time `json:"expires_at"`
}

type claim struct {
	owner, id, token string
	generation       int64
}

const columns = `id::text,repository::text,snapshot,analysis,retrieval,commit_oid,goal,planner,state,generation,deadline,created_at,updated_at,failure,outcome`

func scanRun(row pgx.Row) (r Run, err error) {
	err = row.Scan(&r.ID, &r.Scope.Repository, &r.Scope.Snapshot, &r.Scope.Analysis, &r.Scope.Retrieval, &r.Scope.Commit, &r.Goal, &r.Planner, &r.State, &r.Generation, &r.Deadline, &r.Created, &r.Updated, &r.Failure, &r.Outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return
}

func validRun(owner, id string) bool {
	parsed, err := uuid.Parse(id)
	return len(owner) > 0 && len(owner) <= 200 && err == nil && parsed.String() == id
}

func (s Service) Create(ctx context.Context, owner, index, goal string) (Run, error) {
	if len(owner) == 0 || len(owner) > 200 || len(goal) > 256 || strings.TrimSpace(goal) == "" || strings.ContainsAny(goal, "\x00\r\n") || s.Planner.Identity() == "" || len(s.Planner.Identity()) > 128 {
		return Run{}, ErrPolicy
	}
	scope, err := s.Readers.resolve(ctx, owner, index)
	if err != nil {
		return Run{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('research:'||$1,0))", owner); err != nil {
		return Run{}, err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE deadline>clock_timestamp() AND state IN ('CREATED','RUNNING','PLANNING','WAITING_APPROVAL'))<3
 AND count(*)<12 FROM research_runs WHERE owner=$1 AND created_at>clock_timestamp()-interval '1 hour'`, owner).Scan(&allowed); err != nil {
		return Run{}, err
	}
	if !allowed {
		return Run{}, ErrBudget
	}
	run, err := scanRun(tx.QueryRow(ctx, `INSERT INTO research_runs(owner,repository,snapshot,analysis,retrieval,commit_oid,planner,goal)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+columns, owner, scope.Repository, scope.Snapshot, scope.Analysis, scope.Retrieval, scope.Commit, s.Planner.Identity(), goal))
	if err != nil {
		return Run{}, err
	}
	return run, tx.Commit(ctx)
}

// maintenance and all state-changing operations serialize on the run row.
func maintain(ctx context.Context, tx pgx.Tx, owner, id string) error {
	var expired bool
	var state string
	err := tx.QueryRow(ctx, `SELECT deadline<=clock_timestamp(),state FROM research_runs WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner).Scan(&expired, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if expired && (state == "CREATED" || state == "RUNNING" || state == "PLANNING" || state == "WAITING_APPROVAL") {
		if _, err = tx.Exec(ctx, `UPDATE research_approvals SET state='EXPIRED',decided_at=clock_timestamp() WHERE run_id=$1 AND state='PENDING'`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE research_runs SET state='EXPIRED',claim=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`, id)
		return err
	}
	if state != "WAITING_APPROVAL" {
		return nil
	}
	changed, err := tx.Exec(ctx, `UPDATE research_approvals SET state='EXPIRED',decided_at=clock_timestamp() WHERE run_id=$1 AND state='PENDING' AND expires_at<=clock_timestamp()`, id)
	if err == nil && changed.RowsAffected() > 0 {
		_, err = tx.Exec(ctx, `UPDATE research_runs SET state='EXPIRED',updated_at=clock_timestamp() WHERE id=$1`, id)
	}
	return err
}

func (s Service) View(ctx context.Context, owner, id string) (Run, error) {
	if !validRun(owner, id) {
		return Run{}, ErrDenied
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	if err = maintain(ctx, tx, owner, id); err != nil {
		return Run{}, err
	}
	r, err := scanRun(tx.QueryRow(ctx, `SELECT `+columns+` FROM research_runs WHERE id=$1 AND owner=$2`, id, owner))
	if err != nil {
		return Run{}, err
	}
	r.Steps, err = loadSteps(ctx, tx, id)
	if err != nil {
		return Run{}, err
	}
	rows, err := tx.Query(ctx, `SELECT ordinal,action_sha256,state,requested_by,coalesce(decided_by,''),expires_at FROM research_approvals WHERE run_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return Run{}, err
	}
	for rows.Next() {
		var a Approval
		if err = rows.Scan(&a.Ordinal, &a.Digest, &a.State, &a.Requester, &a.Decider, &a.Expires); err != nil {
			rows.Close()
			return Run{}, err
		}
		r.Approvals = append(r.Approvals, a)
	}
	if err = rows.Err(); err != nil {
		return Run{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return r, nil
}

func loadSteps(ctx context.Context, tx pgx.Tx, id string) ([]Step, error) {
	rows, err := tx.Query(ctx, `SELECT s.ordinal,s.action_sha256,s.decision,s.observation,
 (SELECT count(*) FROM research_attempts a WHERE a.run_id=s.run_id AND a.ordinal=s.ordinal)
 FROM research_steps s WHERE run_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []Step
	for rows.Next() {
		var step Step
		if err = rows.Scan(&step.Ordinal, &step.Digest, &step.Decision, &step.Observation, &step.Attempts); err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func (s Service) updateOwner(ctx context.Context, owner, id string, fn func(pgx.Tx) error) error {
	if !validRun(owner, id) {
		return ErrDenied
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = maintain(ctx, tx, owner, id); err != nil {
		return err
	}
	if err = fn(tx); err != nil && !errors.Is(err, ErrDenied) {
		return err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return commitErr
	}
	return err
}

func (s Service) Cancel(ctx context.Context, owner, id string) error {
	return s.updateOwner(ctx, owner, id, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE research_approvals SET state='CANCELLED',decided_at=clock_timestamp() WHERE run_id=$1 AND state='PENDING'`, id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE research_runs SET state='CANCELLED',claim=NULL,lease_until=NULL,updated_at=clock_timestamp()
   WHERE id=$1 AND state IN ('CREATED','RUNNING','PLANNING','WAITING_APPROVAL')`, id)
		return err
	})
}

func (s Service) Decide(ctx context.Context, owner, id string, ordinal int, digest string, grant bool) error {
	if !validID(digest) || ordinal < 1 || ordinal > MaxSteps {
		return ErrPolicy
	}
	return s.updateOwner(ctx, owner, id, func(tx pgx.Tx) error {
		status, next := "DENIED", "CANCELLED"
		if grant {
			status, next = "GRANTED", "CREATED"
		}
		var previous, decider string
		err := tx.QueryRow(ctx, `SELECT state,coalesce(decided_by,'') FROM research_approvals WHERE run_id=$1 AND ordinal=$2 AND action_sha256=$3 AND requested_by=$4`, id, ordinal, digest, owner).Scan(&previous, &decider)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		allowed, replay := ApprovalTransition(previous, decider, owner, status)
		if replay {
			return nil
		}
		if !allowed {
			return ErrDenied
		}
		changed, err := tx.Exec(ctx, `UPDATE research_approvals SET state=$3,decided_at=clock_timestamp(),decided_by=$4
   WHERE run_id=$1 AND ordinal=$2 AND expires_at>clock_timestamp() AND state='PENDING'`, id, ordinal, status, owner)
		if err != nil {
			return err
		}
		if changed.RowsAffected() != 1 {
			return ErrDenied
		}
		_, err = tx.Exec(ctx, `UPDATE research_runs SET state=$2,updated_at=clock_timestamp() WHERE id=$1 AND state='WAITING_APPROVAL'`, id, next)
		return err
	})
}

func (s Service) acquire(ctx context.Context, owner, id string) (claim, error) {
	c := claim{owner: owner, id: id, token: uuid.NewString()}
	err := s.updateOwner(ctx, owner, id, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `UPDATE research_runs SET state='PLANNING',claim=$3,generation=generation+1,
   lease_until=least(deadline,clock_timestamp()+interval '135 seconds'),updated_at=clock_timestamp()
   WHERE id=$1 AND owner=$2 AND planner=$4 AND deadline>clock_timestamp()
    AND (state='CREATED' OR (state IN ('RUNNING','PLANNING') AND lease_until<=clock_timestamp()))
   RETURNING generation`, id, owner, c.token, s.Planner.Identity()).Scan(&c.generation)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrDenied
		}
		return e
	})
	return c, err
}

func (s Service) fenced(ctx context.Context, c claim, fn func(pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var authorized bool
	err = tx.QueryRow(ctx, `UPDATE research_runs SET lease_until=least(deadline,clock_timestamp()+interval '135 seconds'),updated_at=clock_timestamp()
 WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4 AND lease_until>clock_timestamp() AND deadline>clock_timestamp()
 AND state IN ('PLANNING','RUNNING') RETURNING true`, c.id, c.owner, c.token, c.generation).Scan(&authorized)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT deadline>clock_timestamp() FROM research_runs WHERE id=$1`, c.id).Scan(&authorized); err != nil {
		return err
	}
	if !authorized {
		return ErrDenied
	}
	return tx.Commit(ctx)
}

func (s Service) fail(ctx context.Context, c claim, class string) error {
	return s.fenced(ctx, c, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE research_runs SET state='FAILED',claim=NULL,lease_until=NULL,failure=$2 WHERE id=$1`, c.id, class)
		return err
	})
}

func digestStep(r Run, ordinal int, d Decision) string {
	body, _ := json.Marshal([]any{r.ID, r.Scope, r.Planner, ordinal, d})
	return hash(body)
}

func (s Service) saveDecision(ctx context.Context, c claim, step Step) error {
	return s.fenced(ctx, c, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO research_steps(run_id,ordinal,action_sha256,decision) VALUES($1,$2,$3,$4)`, c.id, step.Ordinal, step.Digest, step.Decision)
		if err != nil {
			return err
		}
		if step.Decision.Kind != "REQUEST_APPROVAL" {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO research_approvals(run_id,ordinal,action_sha256,requested_by,expires_at)
    SELECT id,$2,$3,owner,least(deadline,clock_timestamp()+interval '5 minutes') FROM research_runs WHERE id=$1`, c.id, step.Ordinal, step.Digest)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE research_runs SET state='WAITING_APPROVAL',claim=NULL,lease_until=NULL WHERE id=$1`, c.id)
		return err
	})
}

func (s Service) reserveTool(ctx context.Context, c claim, step Step) error {
	return s.fenced(ctx, c, func(tx pgx.Tx) error {
		if step.Decision.Kind == "REQUEST_APPROVAL" {
			var allowed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM research_approvals WHERE run_id=$1 AND ordinal=$2 AND action_sha256=$3 AND state='GRANTED' AND decided_by=$4 AND expires_at>clock_timestamp())`, c.id, step.Ordinal, step.Digest, c.owner).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return ErrPolicy
			}
		}
		var total, queries int
		if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE s.decision->>'tool' IN ('search_code','inspect_evidence'))
 FROM research_attempts a JOIN research_steps s USING(run_id,ordinal) WHERE a.run_id=$1`, c.id).Scan(&total, &queries); err != nil {
			return err
		}
		query := step.Decision.Tool == "search_code" || step.Decision.Tool == "inspect_evidence"
		if step.Attempts >= 3 || total >= MaxAttempts || (query && queries >= MaxQueries) {
			return ErrBudget
		}
		if _, err := tx.Exec(ctx, `INSERT INTO research_attempts(run_id,ordinal,attempt,generation)
 VALUES($1,$2,$3,$4)`, c.id, step.Ordinal, step.Attempts+1, c.generation); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE research_runs SET state='RUNNING' WHERE id=$1`, c.id)
		return err
	})
}

func (s Service) record(ctx context.Context, c claim, step Step, o Observation, outcome string) error {
	return s.fenced(ctx, c, func(tx pgx.Tx) error {
		changed, err := tx.Exec(ctx, `UPDATE research_steps SET observation=$4 WHERE run_id=$1 AND ordinal=$2 AND action_sha256=$3 AND observation IS NULL`, c.id, step.Ordinal, step.Digest, o)
		if err != nil {
			return err
		}
		if changed.RowsAffected() != 1 {
			return ErrDenied
		}
		if outcome != "" {
			_, err = tx.Exec(ctx, `UPDATE research_runs SET state='COMPLETED',outcome=$2,claim=NULL,lease_until=NULL WHERE id=$1`, c.id, outcome)
		}
		return err
	})
}

func (s Service) reserveModel(ctx context.Context, c claim) error {
	return s.fenced(ctx, c, func(tx pgx.Tx) error {
		updated, err := tx.Exec(ctx, `UPDATE research_runs SET planner_calls=planner_calls+1,state='PLANNING' WHERE id=$1 AND planner_calls<12`, c.id)
		if err == nil && updated.RowsAffected() != 1 {
			return ErrBudget
		}
		return err
	})
}

func (s Service) Resume(ctx context.Context, owner, id string) (err error) {
	start := time.Now()
	defer func() {
		result := "ok"
		if err != nil {
			result = "error"
		}
		telemetry.Observe("research.resume", result, "", time.Since(start).Seconds())
	}()
	c, err := s.acquire(ctx, owner, id)
	if err != nil {
		return err
	}
	for turn := 0; turn < MaxSteps; turn++ {
		r, e := s.View(ctx, owner, id)
		if e != nil {
			return e
		}
		if r.Generation != c.generation || (r.State != "RUNNING" && r.State != "PLANNING") {
			return ErrDenied
		}
		var step Step
		history := r.Steps
		if len(history) > 0 && history[len(history)-1].Observation == nil {
			step = history[len(history)-1]
			history = history[:len(history)-1]
		} else {
			if len(history) >= MaxSteps {
				return s.stop(ctx, c, ErrBudget)
			}
			if e = s.reserveModel(ctx, c); e != nil {
				return s.stop(ctx, c, e)
			}
			stage, cancel := context.WithTimeout(ctx, 5*time.Second)
			output, e := s.Planner.Next(stage, PlannerInput{Goal: r.Goal, History: history})
			if stage.Err() != nil {
				e = stage.Err()
			}
			cancel()
			if e != nil {
				return s.stop(ctx, c, e)
			}
			d, e := decodeDecision(output)
			if e != nil {
				return s.stop(ctx, c, e)
			}
			step = Step{Ordinal: len(history) + 1, Digest: digestStep(r, len(history)+1, d), Decision: d}
			if e = s.saveDecision(ctx, c, step); e != nil {
				return e
			}
			if step.Decision.Kind == "REQUEST_APPROVAL" {
				return nil
			}
		}
		payload, _ := json.Marshal(step.Decision)
		d, e := decodeDecision(payload)
		if e != nil || step.Digest != digestStep(r, step.Ordinal, d) {
			return s.stop(ctx, c, ErrPolicy)
		}
		if d.Kind == "FINISH" {
			outcome := qualifiedOutcome(history, d.Outcome)
			return s.record(ctx, c, step, Observation{Status: outcome, Uncertainty: []string{"citation_integrity_only", "task_sufficiency_unassessed"}}, outcome)
		}
		if e = s.reserveTool(ctx, c, step); e != nil {
			return s.stop(ctx, c, e)
		}
		stage, cancel := context.WithTimeout(ctx, 110*time.Second)
		o, e := s.Readers.read(stage, owner, r.Scope, d)
		cancel()
		if e != nil {
			return s.stop(ctx, c, e)
		}
		bytes, tokens := o.SourceBytes, o.Tokens
		for _, previous := range history {
			bytes += previous.Observation.SourceBytes
			tokens += previous.Observation.Tokens
		}
		body, _ := json.Marshal(o)
		if o.SourceBytes < 0 || o.Tokens < 0 || len(body) > 30000 || bytes > MaxBytes || tokens > MaxTokens {
			return s.stop(ctx, c, ErrBudget)
		}
		if e = s.record(ctx, c, step, o, ""); e != nil {
			return e
		}
	}
	return s.stop(ctx, c, ErrBudget)
}

func qualifiedOutcome(history []Step, requested string) string {
	if requested == "NO_EVIDENCE" {
		return requested
	}
	verified, ready := false, false
	for _, step := range history {
		if step.Observation == nil {
			continue
		}
		if len(step.Observation.Citations) > 0 {
			verified = true
		}
		if step.Decision.Tool == "inspect_evidence" {
			ready = step.Observation.Status == "READY" && len(step.Observation.Citations) > 0
		}
	}
	if !verified {
		return "NO_EVIDENCE"
	}
	if ready && requested == "VERIFIED_REFERENCES" {
		return requested
	}
	return "INSUFFICIENT_EVIDENCE"
}

func (s Service) stop(ctx context.Context, c claim, cause error) error {
	if ctx.Err() != nil || errors.Is(cause, ErrDenied) {
		return cause
	}
	class := "backend"
	if errors.Is(cause, ErrPolicy) {
		class = "policy"
	}
	if errors.Is(cause, ErrBudget) {
		class = "budget"
	}
	if err := s.fail(ctx, c, class); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrBackend, class)
}
