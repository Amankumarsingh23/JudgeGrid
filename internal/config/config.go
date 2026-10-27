package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	RedisAddr          string
	RedisPassword      string
	CheckTimeout       time.Duration
	RequestTimeout     time.Duration
	AppEnv             string
	DevAuthEnabled     bool
	DevPrincipal       string
	OutboxBacklogLimit int
}

func Load() (Config, error) {
	c := Config{HTTPAddr: env("HTTP_ADDR", ":8080"), DatabaseURL: env("DATABASE_URL", "postgres://judgegrid:judgegrid@localhost:5432/judgegrid?sslmode=disable"), RedisAddr: env("REDIS_ADDR", "localhost:6379"), RedisPassword: os.Getenv("REDIS_PASSWORD")}
	var err error
	c.CheckTimeout, err = time.ParseDuration(env("CHECK_TIMEOUT", "3s"))
	if err != nil || c.CheckTimeout <= 0 {
		return Config{}, fmt.Errorf("CHECK_TIMEOUT must be a positive duration")
	}
	c.RequestTimeout, err = time.ParseDuration(env("REQUEST_TIMEOUT", "5s"))
	if err != nil || c.RequestTimeout <= 0 || c.RequestTimeout > 8*time.Second {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT must be positive and at most 8s")
	}
	c.AppEnv = env("APP_ENV", "production")
	c.DevAuthEnabled, err = strconv.ParseBool(env("DEV_AUTH_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("DEV_AUTH_ENABLED must be a boolean")
	}
	c.DevPrincipal = os.Getenv("DEV_PRINCIPAL")
	c.OutboxBacklogLimit, err = strconv.Atoi(env("OUTBOX_BACKLOG_LIMIT", "10000"))
	if err != nil || c.OutboxBacklogLimit < 1 || c.OutboxBacklogLimit > 100000 {
		return Config{}, fmt.Errorf("OUTBOX_BACKLOG_LIMIT must be 1..100000")
	}
	if c.DevAuthEnabled && (c.AppEnv != "development" || strings.TrimSpace(c.DevPrincipal) == "" || len(c.DevPrincipal) > 200) {
		return Config{}, fmt.Errorf("development auth requires APP_ENV=development and DEV_PRINCIPAL of 1..200 characters")
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
