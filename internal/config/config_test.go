package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("CHECK_TIMEOUT", "2s")
	c, err := Load()
	if err != nil || c.HTTPAddr != ":9090" || c.CheckTimeout.Seconds() != 2 {
		t.Fatalf("unexpected config: %+v %v", c, err)
	}
	for _, value := range []string{"invalid", "0s", "-1s"} {
		t.Setenv("CHECK_TIMEOUT", value)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
