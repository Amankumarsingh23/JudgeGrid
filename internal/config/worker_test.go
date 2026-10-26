package config

import "testing"

func TestWorkerConfiguration(t *testing.T) {
	t.Setenv("DEV_DISPATCH_ENABLED", "false")
	t.Setenv("QUEUE_STREAM", "submissions:jobs")
	w, err := LoadWorker("production")
	if err != nil || w.Enabled || w.Options.Concurrency != 4 {
		t.Fatalf("%+v %v", w, err)
	}
	t.Setenv("DEV_DISPATCH_ENABLED", "true")
	if _, err := LoadWorker("production"); err == nil {
		t.Fatal("production allowed test consumption")
	}
	if _, err := LoadWorker("development"); err == nil {
		t.Fatal("test handler allowed production stream")
	}
	t.Setenv("QUEUE_STREAM", "dev:dispatch")
	if _, err := LoadWorker("development"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{{"WORKER_CONCURRENCY", "0"}, {"WORKER_CONCURRENCY", "65"}, {"WORKER_BLOCK", "0s"}, {"WORKER_BLOCK", "6s"}, {"WORKER_MIN_IDLE", "-1s"}, {"WORKER_RECOVERY_INTERVAL", "bad"}, {"WORKER_SHUTDOWN_GRACE", "0s"}, {"WORKER_BACKOFF_MIN", "10s"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadWorker("development"); err == nil {
				t.Fatal("invalid worker setting accepted")
			}
		})
	}
}
