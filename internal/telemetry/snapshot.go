package telemetry

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

// Only the API process owns the cluster DB gauges. A single 5s background query
// caps each inventory at 10001 rows; scrape handlers never query PostgreSQL.
func Poll(ctx context.Context, db *pgxpool.Pool) {
	if !Enabled() {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, time.Second)
		rows, e := db.Query(check, `
SELECT 'outbox',s.status,count(q.id)::float8,count(q.id)=10001 FROM(VALUES('PENDING'),('CLAIMED'),('PUBLISHED'),('QUARANTINED'))s(status) LEFT JOIN LATERAL(SELECT id FROM outbox_events e WHERE e.status=s.status LIMIT 10001)q ON true GROUP BY s.status
UNION ALL SELECT 'outbox_age','PENDING',COALESCE(GREATEST(0,EXTRACT(EPOCH FROM clock_timestamp()-min(created_at))),0)::float8,false FROM(SELECT created_at FROM outbox_events WHERE status IN('PENDING','CLAIMED') ORDER BY created_at LIMIT 10001)q
UNION ALL SELECT 'scheduler',CASE WHEN done THEN 'COMPLETED' ELSE 'PENDING' END,count(*)::float8,count(*)=10001 FROM(SELECT done FROM scheduler_jobs WHERE NOT done LIMIT 10001)q GROUP BY done
UNION ALL SELECT 'leases',CASE WHEN lease_expires_at>clock_timestamp() THEN 'ACTIVE' ELSE 'EXPIRED' END,count(*)::float8,count(*)=10001 FROM(SELECT lease_expires_at FROM sandbox_authority WHERE state='ACTIVE' LIMIT 10001)q GROUP BY 2
UNION ALL SELECT 'sse','ACTIVE',count(*)::float8,count(*)=10001 FROM(SELECT id FROM sse_connections WHERE expires_at>clock_timestamp() LIMIT 10001)q
`)
		if e == nil {
			values := map[string]float64{}
			capped := false
			for rows.Next() {
				var k, s string
				var n float64
				var cap bool
				if rows.Scan(&k, &s, &n, &cap) != nil {
					e = rows.Err()
					break
				}
				values[k+":"+s] = n
				capped = capped || cap
			}
			e = rows.Err()
			rows.Close()
			if e == nil {
				for _, pair := range []struct{ k, states string }{{"outbox", "PENDING CLAIMED PUBLISHED QUARANTINED"}, {"outbox_age", "PENDING"}, {"scheduler", "PENDING"}, {"leases", "ACTIVE EXPIRED"}, {"sse", "ACTIVE"}} {
					for _, s := range strings.Fields(pair.states) {
						Gauge(pair.k, s, values[pair.k+":"+s])
					}
				}
				Gauge("snapshot_ok", "ok", 1)
				Gauge("snapshot_capped", "ok", boolFloat(capped))
				Gauge("snapshot_age", "ok", float64(time.Now().Unix()))
			}
		}
		cancel()
		Count("backend.snapshot", Class(e), "")
		if e != nil {
			Gauge("snapshot_ok", "ok", 0)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
