package queue

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrMalformed = errors.New("malformed dispatch message")
var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const (
	SubmissionJob   = "submission"
	RejudgeJob      = "rejudge"
	DispatchTestJob = "dispatch_test"
)

// Job is a reference, never executable source. Attempt is not yet an ownership/fencing token.
type Job struct {
	JobID        string
	Kind         string
	SubmissionID string
	Attempt      int64
	EventID      string
}

func (j Job) Validate() error {
	if j.Kind == RejudgeJob {
		if !uuid.MatchString(j.JobID) || !uuid.MatchString(j.SubmissionID) || !uuid.MatchString(j.EventID) || j.Attempt != 1 {
			return ErrMalformed
		}
		return nil
	}
	if j.JobID != "" {
		return ErrMalformed
	}
	if !uuid.MatchString(j.SubmissionID) || j.Attempt < 1 || (j.Kind != SubmissionJob && j.Kind != DispatchTestJob) || (j.EventID != "" && (!uuid.MatchString(j.EventID) || j.Kind != SubmissionJob)) {
		return ErrMalformed
	}
	return nil
}
func (j Job) Values() map[string]any {
	if j.Kind == RejudgeJob {
		return map[string]any{"schema_version": "3", "kind": j.Kind, "submission_id": strings.ToLower(j.SubmissionID), "attempt": "1", "event_id": strings.ToLower(j.EventID), "job_id": strings.ToLower(j.JobID)}
	}
	if j.EventID != "" {
		return map[string]any{"schema_version": "2", "kind": j.Kind, "submission_id": strings.ToLower(j.SubmissionID), "attempt": strconv.FormatInt(j.Attempt, 10), "event_id": strings.ToLower(j.EventID)}
	}
	return map[string]any{"schema_version": "1", "kind": j.Kind, "submission_id": strings.ToLower(j.SubmissionID), "attempt": strconv.FormatInt(j.Attempt, 10)}
}

type Message struct {
	ID     string
	Values map[string]any
}

func (m Message) Job() (Job, error) {
	if m.Values["schema_version"] == "3" {
		if len(m.Values) != 6 {
			return Job{}, ErrMalformed
		}
		v := func(k string) string { s, _ := m.Values[k].(string); return strings.ToLower(s) }
		j := Job{Kind: v("kind"), SubmissionID: v("submission_id"), EventID: v("event_id"), JobID: v("job_id"), Attempt: 1}
		if v("attempt") != "1" || j.Kind != RejudgeJob {
			return Job{}, ErrMalformed
		}
		return j, j.Validate()
	}
	if len(m.Values) != 4 && len(m.Values) != 5 {
		return Job{}, ErrMalformed
	}
	value := func(key string) string { s, _ := m.Values[key].(string); return s }
	version := value("schema_version")
	if (version != "1" || len(m.Values) != 4) && (version != "2" || len(m.Values) != 5 || !uuid.MatchString(value("event_id"))) {
		return Job{}, ErrMalformed
	}
	attempt, err := strconv.ParseInt(value("attempt"), 10, 64)
	if err != nil {
		return Job{}, ErrMalformed
	}
	j := Job{Kind: value("kind"), SubmissionID: strings.ToLower(value("submission_id")), Attempt: attempt}
	if version == "2" {
		j.EventID = strings.ToLower(value("event_id"))
	}
	return j, j.Validate()
}

type Streams struct {
	client *redis.Client
	Stream string
	Group  string
}

func (c *Client) Streams(stream, group string) *Streams {
	return &Streams{client: c.client, Stream: stream, Group: group}
}
func (s *Streams) EnsureGroup(ctx context.Context) error {
	// Start at 0 to include entries published before group initialization.
	err := s.client.XGroupCreateMkStream(ctx, s.Stream, s.Group, "0").Err()
	if err != nil && strings.HasPrefix(err.Error(), "BUSYGROUP ") {
		return nil
	}
	return err
}
func (s *Streams) Publish(ctx context.Context, j Job) (string, error) {
	if err := j.Validate(); err != nil {
		return "", err
	}
	return s.client.XAdd(ctx, &redis.XAddArgs{Stream: s.Stream, Values: j.Values()}).Result()
}
func (s *Streams) Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]Message, error) {
	if count < 1 || count > 100 || block < time.Millisecond {
		return nil, fmt.Errorf("invalid bounded stream read")
	}
	rows, err := s.client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: s.Group, Consumer: consumer, Streams: []string{s.Stream, ">"}, Count: count, Block: block}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []Message
	for _, row := range rows {
		for _, m := range row.Messages {
			result = append(result, Message{ID: m.ID, Values: m.Values})
		}
	}
	return result, nil
}
func (s *Streams) Ack(ctx context.Context, id string) error {
	return s.client.XAck(ctx, s.Stream, s.Group, id).Err()
}
func (s *Streams) Pending(ctx context.Context) (*redis.XPending, error) {
	return s.client.XPending(ctx, s.Stream, s.Group).Result()
}
func (s *Streams) CheckGroup(ctx context.Context) error {
	groups, err := s.client.XInfoGroups(ctx, s.Stream).Result()
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.Name == s.Group {
			return nil
		}
	}
	return fmt.Errorf("consumer group unavailable")
}
func (s *Streams) PendingEntries(ctx context.Context, start string, count int64) ([]redis.XPendingExt, error) {
	if count < 1 || count > 100 {
		return nil, fmt.Errorf("invalid pending count")
	}
	return s.client.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: s.Stream, Group: s.Group, Start: start, End: "+", Count: count}).Result()
}
func (s *Streams) AutoClaim(ctx context.Context, consumer, cursor string, minIdle time.Duration, count int64) ([]Message, string, error) {
	if minIdle < time.Millisecond || count < 1 || count > 100 {
		return nil, "", fmt.Errorf("invalid bounded claim")
	}
	entries, next, err := s.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: s.Stream, Group: s.Group, Consumer: consumer, MinIdle: minIdle, Start: cursor, Count: count}).Result()
	if err != nil {
		return nil, "", err
	}
	result := make([]Message, 0, len(entries))
	for _, m := range entries {
		result = append(result, Message{ID: m.ID, Values: m.Values})
	}
	return result, next, nil
}

func IsDevelopmentStream(name string) bool {
	return (strings.HasPrefix(name, "dev:") || strings.HasPrefix(name, "test:")) && len(name) <= 200
}
