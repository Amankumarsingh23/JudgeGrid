package config

import "testing"

func TestRelayConfiguration(t *testing.T) {
	t.Setenv("OUTBOX_RELAY_ENABLED", "false")
	c, e := LoadRelay()
	if e != nil || c.Enabled {
		t.Fatal(c, e)
	}
	t.Setenv("OUTBOX_RELAY_ENABLED", "true")
	t.Setenv("OUTBOX_BATCH", "16")
	if c, e = LoadRelay(); e != nil || !c.Enabled {
		t.Fatal(c, e)
	}
	t.Setenv("OUTBOX_PUBLISH_TIMEOUT", "30s")
	if _, e = LoadRelay(); e == nil {
		t.Fatal("deadline outside claim accepted")
	}
}
