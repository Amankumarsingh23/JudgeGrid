package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	CheckTimeout  time.Duration
}

func Load() (Config, error) {
	c := Config{HTTPAddr: env("HTTP_ADDR", ":8080"), DatabaseURL: env("DATABASE_URL", "postgres://judgegrid:judgegrid@localhost:5432/judgegrid?sslmode=disable"), RedisAddr: env("REDIS_ADDR", "localhost:6379"), RedisPassword: os.Getenv("REDIS_PASSWORD")}
	var err error
	c.CheckTimeout, err = time.ParseDuration(env("CHECK_TIMEOUT", "3s"))
	if err != nil || c.CheckTimeout <= 0 {
		return Config{}, fmt.Errorf("CHECK_TIMEOUT must be a positive duration")
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
