package execution

import (
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
)

func (d *Docker) ConfigureCache(db *pgxpool.Pool) error {
	if os.Getenv("COMPILATION_CACHE_ENABLED") == "" || os.Getenv("COMPILATION_CACHE_ENABLED") == "false" {
		return nil
	}
	if os.Getenv("COMPILATION_CACHE_ENABLED") != "true" || os.Getenv("BUNDLE_BACKEND") != "s3" {
		return fmt.Errorf("cache requires explicit S3 configuration")
	}
	s, e := objects.New(os.Getenv("S3_ENDPOINT"), os.Getenv("S3_CACHE_BUCKET"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), os.Getenv("S3_TLS") != "false")
	if e != nil {
		return e
	}
	d.SetCache(&buildcache.Cache{DB: db, Objects: s})
	return nil
}
