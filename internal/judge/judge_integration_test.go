//go:build judgeintegration

package judge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealJudgeMatrix(t *testing.T) {
	if os.Getenv("JUDGEGRID_JUDGE_TESTS") != "1" || os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("requires explicit JUDGEGRID_JUDGE_TESTS=1 and TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("judgegrid_real_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = storage.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := bundles.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.SeedSuites(ctx, pool, store); err != nil {
		t.Fatal(err)
	}
	root := "."
	if _, err = os.Stat("infra/sandbox/seccomp.json"); err != nil {
		root = "../.."
	}
	images, err := execution.LoadImages(filepath.Join(root, ".sandbox-images.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := execution.NewDocker(images, filepath.Join(root, "infra/sandbox/seccomp.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	repo := storage.NewRepository(pool)
	service := judge.Service{Repository: repo, Bundles: store, Engine: engine}
	var problem int64
	if err = pool.QueryRow(ctx, `SELECT id FROM problems WHERE slug='two-sum'`).Scan(&problem); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Routes(time.Second, pool.Ping, func(context.Context) error { return nil }, submission.New(repo, 8*time.Second), api.DevelopmentPrincipal(true, "alice")))
	defer server.Close()
	for _, tc := range []struct {
		name, lang, source string
		want               domain.Verdict
		runs               int
	}{
		{"cpp-ac", "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}", "AC", 6},
		{"python-ac", "python3", "a,b=map(int,input().split()); print(a+b)", "AC", 6},
		{"wrong", "python3", "print(999)", "WA", 1},
		{"syntax", "cpp17", "int main( {", "CE", 0},
		{"runtime", "python3", "raise RuntimeError('private diagnostics')", "RE", 1},
		{"timeout", "python3", "while True: pass", "TLE", 1},
		{"memory", "python3", "x=bytearray(512*1024*1024); print(len(x))", "MLE", 1},
		{"stdout", "python3", "import os\nwhile True: os.write(1,b'x'*8192)", "OLE", 1},
		{"stderr", "python3", "import os\nwhile True: os.write(2,b'x'*8192)", "OLE", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub, _, e := repo.Create(ctx, "alice", "real-"+tc.name, domain.CreateRequest{ProblemID: problem, Language: tc.lang, SourceCode: tc.source})
			if e != nil {
				t.Fatal(e)
			}
			before := engine.Counts()
			v, e := service.Run(ctx, "alice", sub.ID)
			if e != nil || v.Verdict == nil || *v.Verdict != tc.want || v.TestsRun != tc.runs {
				t.Fatalf("summary=%+v error=%v", v, e)
			}
			after := engine.Counts()
			if tc.lang == "cpp17" && after.Compilations-before.Compilations != 1 {
				t.Fatal("C++ compilation count", before, after)
			}
			if after.RuntimeStarts-before.RuntimeStarts != int64(tc.runs) {
				t.Fatal("runtime count", before, after)
			}
			again, e := service.Run(ctx, "alice", sub.ID)
			if e != nil || !again.Replayed || again.RunID != v.RunID || engine.Counts() != after {
				t.Fatal("replay reexecuted", again, e)
			}
			var rows int
			var status string
			var verdict domain.Verdict
			if e = pool.QueryRow(ctx, `SELECT status,verdict,(SELECT count(*) FROM evaluation_test_results WHERE evaluation_run_id=$2) FROM submissions WHERE id=$1`, sub.ID, v.RunID).Scan(&status, &verdict, &rows); e != nil || rows != tc.runs || status != "COMPLETED" || verdict != tc.want {
				t.Fatal(status, verdict, rows, e)
			}
			response, e := http.Get(server.URL + "/api/v1/submissions/" + sub.ID)
			if e != nil {
				t.Fatal(e)
			}
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			var public map[string]any
			if json.Unmarshal(data, &public) != nil || response.StatusCode != 200 {
				t.Fatal(string(data))
			}
			for _, hidden := range []string{"expected_sha", "bundle_sha", "case-a", "-1000000000 -1000000000", `"stderr"`, "stdout_sha"} {
				if strings.Contains(string(data), hidden) {
					t.Fatal("private evidence leaked")
				}
			}
			t.Logf("verdict=%s tests=%d passed=%d compilations=%d runtime_starts=%d version=%d", tc.want, v.TestsRun, v.TestsPassed, after.Compilations-before.Compilations, after.RuntimeStarts-before.RuntimeStarts, v.ProblemVersionID)
		})
	}
	// New immutable versions exercise empty input, fresh /tmp and checker policy.
	for _, policy := range []string{"tokens", "exact"} {
		t.Run(policy+"-empty-isolation", func(t *testing.T) {
			var version int64
			err = pool.QueryRow(ctx, `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) SELECT problem_id,version_number+10,statement,time_limit_ms,memory_limit_kb FROM problem_versions WHERE id=(SELECT max(id) FROM problem_versions WHERE problem_id=$1) RETURNING id`, problem).Scan(&version)
			if err != nil {
				t.Fatal(err)
			}
			suite := bundles.Bundle{Schema: 1, Comparator: policy, Tests: []bundles.Case{{ID: "empty", Order: 1, Expected: []byte("5\n")}, {ID: "fresh", Order: 2, Expected: []byte("5\n")}}}
			ref, e := store.Put(suite)
			if e != nil {
				t.Fatal(e)
			}
			if e = storage.BindSuite(ctx, pool, version, ref); e != nil {
				t.Fatal(e)
			}
			if _, e = pool.Exec(ctx, `UPDATE problems SET current_version_id=$2 WHERE id=$1`, problem, version); e != nil {
				t.Fatal(e)
			}
			source := "import os,sys\nassert sys.stdin.read()==''\nassert not os.path.exists('/tmp/marker')\nopen('/tmp/marker','w').write('x')\nprint(' 5  ')"
			sub, _, e := repo.Create(ctx, "alice", "policy-"+policy, domain.CreateRequest{ProblemID: problem, Language: "python3", SourceCode: source})
			if e != nil {
				t.Fatal(e)
			}
			v, e := service.Run(ctx, "alice", sub.ID)
			want := domain.Verdict("AC")
			runs := 2
			if policy == "exact" {
				want = "WA"
				runs = 1
			}
			if e != nil || v.Verdict == nil || *v.Verdict != want || v.TestsRun != runs || v.ProblemVersionID != version {
				t.Fatal(v, e)
			}
			t.Logf("policy=%s verdict=%s tests=%d empty input and immutable version=%d", policy, want, runs, version)
		})
	}
}
