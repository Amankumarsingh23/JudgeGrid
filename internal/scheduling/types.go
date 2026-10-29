// Package scheduling defines trusted shared admission policy, independently of execution.
package scheduling

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
)

type Policy struct {
	Enabled         bool     `json:"enabled"`
	GlobalActive    int      `json:"global_active"`
	PrincipalActive int      `json:"principal_active"`
	Pending         int      `json:"pending_limit"`
	Burst           int      `json:"rate_burst"`
	Rate            float64  `json:"rate_per_second"`
	SSEGlobal       int      `json:"sse_global"`
	SSEPrincipal    int      `json:"sse_principal"`
	Principals      []string `json:"principals"`
}

func Default() Policy {
	return Policy{GlobalActive: 4, PrincipalActive: 1, Pending: 128, Burst: 120, Rate: 2, SSEGlobal: 32, SSEPrincipal: 2}
}
func (p Policy) Validate() error {
	if p.GlobalActive < 1 || p.GlobalActive > 16 || p.PrincipalActive < 1 || p.PrincipalActive > p.GlobalActive || (p.GlobalActive > 1 && p.PrincipalActive == p.GlobalActive) || p.Pending < 1 || p.Pending > 10000 || p.Burst < 1 || p.Burst > 1000 || p.Rate < .01 || p.Rate > 100 || p.SSEGlobal < 1 || p.SSEGlobal > 128 || p.SSEPrincipal < 1 || p.SSEPrincipal > 8 || len(p.Principals) > 128 || (p.Enabled && len(p.Principals) == 0) {
		return errors.New("invalid bounded scheduler policy")
	}
	seen := map[string]bool{}
	for _, s := range p.Principals {
		if len(s) < 1 || len(s) > 200 || seen[s] {
			return errors.New("invalid scheduler scope")
		}
		seen[s] = true
	}
	return nil
}

type Candidate struct {
	Principal string
	Job       queue.Job
}
type Store interface {
	NextScheduled(context.Context, []string) (*Candidate, error)
	ValidateNotification(context.Context, []string, queue.Job) error
}
