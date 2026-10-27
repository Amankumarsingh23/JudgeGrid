package config

import (
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"strconv"
	"strings"
	"time"
)

type Relay struct {
	Enabled            bool
	Stream, HealthAddr string
	Options            outbox.Options
}

func LoadRelay() (Relay, error) {
	c := Relay{Stream: env("OUTBOX_STREAM", "submissions:jobs"), HealthAddr: env("OUTBOX_HEALTH_ADDR", ":8082"), Options: outbox.DefaultOptions()}
	var err error
	c.Enabled, err = strconv.ParseBool(env("OUTBOX_RELAY_ENABLED", "false"))
	if err != nil {
		return c, fmt.Errorf("invalid OUTBOX_RELAY_ENABLED")
	}
	if len(c.Stream) < 1 || len(c.Stream) > 200 || strings.ContainsAny(c.Stream, " \r\n\t") {
		return c, fmt.Errorf("invalid OUTBOX_STREAM")
	}
	for name, target := range map[string]*int{"OUTBOX_BATCH": &c.Options.Batch, "OUTBOX_MAX_ATTEMPTS": &c.Options.MaxAttempts} {
		*target, err = strconv.Atoi(env(name, strconv.Itoa(*target)))
		if err != nil {
			return c, fmt.Errorf("invalid %s", name)
		}
	}
	for name, target := range map[string]*time.Duration{"OUTBOX_CLAIM_TTL": &c.Options.ClaimTTL, "OUTBOX_PUBLISH_TIMEOUT": &c.Options.PublishTimeout, "OUTBOX_POLL": &c.Options.Poll, "OUTBOX_BACKOFF_MIN": &c.Options.BackoffMin, "OUTBOX_BACKOFF_MAX": &c.Options.BackoffMax, "OUTBOX_DRAIN": &c.Options.Drain} {
		*target, err = time.ParseDuration(env(name, target.String()))
		if err != nil {
			return c, fmt.Errorf("invalid %s", name)
		}
	}
	return c, c.Options.Validate()
}
