package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("DEV_AUTH_ENABLED", "false")
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

func TestDevelopmentIdentityGuard(t *testing.T) {
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("DEV_PRINCIPAL", "local-user")
	t.Setenv("APP_ENV", "production")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted development identity")
	}
	t.Setenv("APP_ENV", "development")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_PRINCIPAL", "")
	if _, err := Load(); err == nil {
		t.Fatal("empty identity accepted")
	}
}
