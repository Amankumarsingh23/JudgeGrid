//go:build integration

package tests

import (
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	var r *telemetry.Runtime
	if os.Getenv("S11_TEST_TELEMETRY") == "1" {
		var e error
		r, e = telemetry.Start("test")
		if e != nil {
			fmt.Fprintln(os.Stderr, "invalid explicit test telemetry setup")
			os.Exit(2)
		}
	}
	code := m.Run()
	if r != nil {
		r.Close()
	}
	os.Exit(code)
}
