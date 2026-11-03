package gormstore

// Option configures a RunStore at construction.
type Option func(*config)

type config struct {
	skipAutoMigrate bool
}

// WithoutAutoMigrate disables the AutoMigrate call at store
// construction. Use it in production when an out-of-band migration tool
// manages schema changes. AutoMigrate is the default for dev and demos. It
// is not meant for long-term schema evolution.
func WithoutAutoMigrate() Option {
	return func(c *config) { c.skipAutoMigrate = true }
}
