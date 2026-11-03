// Package surfaces holds wiring for the agent host surfaces. The terminal
// chat in surfaces/chat and the web bridge in surfaces/web both use it. Its
// construction helpers don't depend on any one surface, so each surface picks
// a backend from one spec string instead of re-deriving the mapping.
package surfaces

import (
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/panyam/chakra"
	gormstore "github.com/panyam/chakra/store/gorm"
	redisstore "github.com/panyam/chakra/store/redis"
)

// RunStoreFromSpec maps a session-store spec to an agent.RunStore backend.
// It accepts these specs.
//
//   - "" (empty)            persistence off; returns (nil, nil) so the caller
//     leaves the host on its non-persisting default.
//   - "memory"              in-process resume/fork that dies with the process.
//   - "sqlite://path.db"    restart-surviving sessions on a local file, no
//     server required.
//   - "redis://host:port"   restart-surviving sessions in a running Redis.
//   - "postgres://user:pass@host:port/db"
//     (or "postgresql://…")  restart-surviving sessions in a running Postgres.
//
// An unknown spec is an error. The chat and web surfaces share the mapping,
// so one --session-store flag means the same thing in both.
func RunStoreFromSpec(spec string) (agent.RunStore, error) {
	switch {
	case spec == "":
		return nil, nil
	case spec == "memory":
		return agent.NewInMemoryRunStore(), nil
	case strings.HasPrefix(spec, "redis://"):
		addr := strings.TrimPrefix(spec, "redis://")
		if addr == "" {
			return nil, fmt.Errorf("surfaces: --session-store redis:// needs host:port")
		}
		return redisstore.New(redis.NewClient(&redis.Options{Addr: addr})), nil
	case strings.HasPrefix(spec, "sqlite://"):
		path := strings.TrimPrefix(spec, "sqlite://")
		if path == "" {
			return nil, fmt.Errorf("surfaces: --session-store sqlite:// needs a file path")
		}
		return openGormStore(sqlite.Open(path + "?_busy_timeout=5000"))
	case strings.HasPrefix(spec, "postgres://") || strings.HasPrefix(spec, "postgresql://"):
		// gorm's postgres driver accepts the URL DSN verbatim.
		return openGormStore(postgres.Open(spec))
	default:
		return nil, fmt.Errorf("surfaces: unknown --session-store %q (want memory, sqlite://path.db, redis://host:port, or postgres://user:pass@host:port/db)", spec)
	}
}

// openGormStore opens the dialector with SQL logging silenced and wraps it in
// the RunStore. A surface's transcript is its output, and slow-query noise
// does not belong there.
func openGormStore(dial gorm.Dialector) (agent.RunStore, error) {
	db, err := gorm.Open(dial, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("surfaces: opening store: %w", err)
	}
	return gormstore.New(db)
}
