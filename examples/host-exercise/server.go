package main

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing/fstest"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/experimental/ext/events"
	skills "github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// What the demo server declares. The checks look for these exact strings on
// the host side, so each one is distinctive enough not to turn up by accident.
const (
	demoInstructions = "Release desk: always check the build status before drafting release notes."
	demoAgent        = "researcher"
	demoAgentReply   = "researcher: two open regressions"
	demoEvent        = "build.finished"
	demoHintTemplate = "build {{status}} on {{branch}}"
	rotatingSkill    = "release-notes"
)

// skillFiles is the demo skills tree. release-notes is the one rotate swaps.
var skillFiles = map[string]string{
	"release-notes/SKILL.md": skillMD(rotatingSkill, "Draft release notes from merged PRs", "List merged PRs, group them by area, write one line each."),
	"triage/SKILL.md":        skillMD("triage", "Triage a new issue", "Label it, give it a priority, link it to a mission."),
	"bisect/SKILL.md":        skillMD("bisect", "Find the commit that broke a test", "Run git bisect with the failing test as the predicate."),
}

func skillMD(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body + "\n"
}

// buildStatus is the payload of the demo event.
type buildStatus struct {
	Status string `json:"status"`
	Branch string `json:"branch"`
}

// demo is one host-exercise server: instructions, skills, an event type with a
// context hint in its _meta, and an agent, all on one MCP endpoint. It counts
// SKILL.md reads so the no-prefetch check can see what the host fetched.
type demo struct {
	handler    http.Handler
	skills     *skills.Provider
	fs         *lockedFS
	yield      func(context.Context, buildStatus) error
	skillReads atomic.Int32
}

func newDemo() (*demo, error) {
	d := &demo{fs: newLockedFS(skillFiles)}
	srv := server.NewServer(core.ServerInfo{Name: "host-exercise", Version: "0.1.0"},
		server.WithInstructions(demoInstructions),
		server.WithMiddleware(func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
			if req.Method == "resources/read" && strings.Contains(string(req.Params.Raw()), "SKILL.md") {
				d.skillReads.Add(1)
			}
			return next(ctx, req)
		}))

	p, err := skills.NewProvider(skills.WithFS(d.fs))
	if err != nil {
		return nil, err
	}
	p.RegisterWith(srv)
	d.skills = p

	src, yield := events.NewYieldingSource[buildStatus](events.EventDef{
		Name:        demoEvent,
		Description: "a CI build finished",
		Meta: map[string]any{agent.MetaKeyContextHint: map[string]any{
			"template": demoHintTemplate,
		}},
	})
	events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Server:                   srv,
		UnsafeAnonymousPrincipal: "host-exercise",
	})
	d.yield = yield

	srv.RegisterTool(core.ToolDef{Name: "list_regressions", Description: "list open regressions", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("two open regressions"), nil
		})
	if _, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{{
		Name:         demoAgent,
		Description:  "looks things up before a release",
		Instructions: "you research the release",
		Tools:        []string{"list_regressions"},
	}}}); err != nil {
		return nil, err
	}

	d.handler = srv.Handler(server.WithStreamableHTTP(true))
	return d, nil
}

// rotate changes the rotating skill's body, which changes its digest in the
// skills/list entry, and tells the provider so the next listing is fresh.
func (d *demo) rotate() error {
	d.fs.set("release-notes/SKILL.md", skillMD(rotatingSkill, "Draft release notes from merged PRs", "Rotated: also email the notes to everyone on the team."))
	return d.skills.Refresh()
}

// tick yields a build event every interval, so a live session has occurrences
// to inject. It runs until the process exits.
func (d *demo) tick(every time.Duration) {
	statuses := []string{"passed", "failed"}
	for i := 0; ; i++ {
		time.Sleep(every)
		_ = d.yield(context.Background(), buildStatus{Status: statuses[i%2], Branch: "main"})
	}
}

// lockedFS is a MapFS that can change while the provider reads it. Each set
// installs a new MapFile rather than editing one in place, so a file already
// open keeps the bytes it was opened with.
type lockedFS struct {
	mu sync.RWMutex
	m  fstest.MapFS
}

func newLockedFS(files map[string]string) *lockedFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return &lockedFS{m: m}
}

func (l *lockedFS) Open(name string) (fs.File, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.m.Open(name)
}

func (l *lockedFS) set(name, body string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[name] = &fstest.MapFile{Data: []byte(body)}
}
