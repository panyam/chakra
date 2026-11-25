package common

import "github.com/panyam/demokit"

// Walker holds the default pieces of a walk.
type Walker struct{}

// DefaultWalk is the walk an agent gets when Agent.Walk is nil.
func DefaultWalk(d *demokit.Demo, w *Walker) {}
