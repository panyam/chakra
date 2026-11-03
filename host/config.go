package host

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
)

// Config is agentchat's JSON configuration. Secrets are never inlined.
// Instead apiKeyEnv / authTokenEnv name environment variables to read at
// startup. The rest of the repo's example configs use the same indirection.
type Config struct {
	// Model configures the OpenAI-compatible endpoint.
	Model ModelConfig `json:"model"`

	// Instructions is the system prompt. Optional.
	Instructions string `json:"instructions,omitempty"`

	// MaxSteps caps model calls per turn. Zero uses the agent default.
	MaxSteps int `json:"maxSteps,omitempty"`

	// StepBudgetWithin is a threshold on the steps a turn has left. Once a
	// turn has that many steps left or fewer, each call tells the model its
	// remaining step budget (see agent.StepBudgetStage). Zero uses the default, a
	// quarter of MaxSteps and at least 3. Negative turns the note off.
	StepBudgetWithin int `json:"stepBudgetWithin,omitempty"`

	// MaxTreeSteps and MaxTreeTokens cap the AGGREGATE model steps / tokens
	// across a turn's whole sub-agent tree. The tree counts the parent, its
	// sub-agents, fan-out members and handoff rounds, so these caps reach
	// further than per-Runner MaxSteps. Zero means unbounded. They guard
	// against deep or chatty multi-agent trees.
	MaxTreeSteps  int `json:"maxTreeSteps,omitempty"`
	MaxTreeTokens int `json:"maxTreeTokens,omitempty"`

	// MaxEventLogRetention bounds the in-memory session event log. The log
	// is a gocurrent.Queue, emit appends to it, and Subscribe replays it.
	// Zero is the default and keeps the log unbounded, as it was before this
	// bound existed, for embedders that never need eviction. A positive value
	// caps the retained window to that many entries. The oldest are evicted
	// as the session runs, so a long-lived process bounds its memory.
	// Eviction is lossless for a persisted session. Subscribe deep-replays
	// turns already written to the RunStore, so a subscriber attaching after
	// eviction still sees full history. Set it generously enough that a
	// single turn never exceeds the window (agentchat defaults to 100000 via
	// --max-event-log).
	MaxEventLogRetention int `json:"maxEventLogRetention,omitempty"`

	// Interruptible lets a sub-agent's mid-flight Signal break the main
	// agent's fan-out join barrier (issue 1167). The first signal cancels the
	// remaining in-flight calls, and the model re-plans with the partial
	// results instead of waiting for every sub-agent. Only useful with
	// signalling sub-agents (SubAgents + CanSignal). False (the default)
	// keeps the pure fan-out-then-join. Leave SignalPolicy unset (inject)
	// alongside it, so the turn re-plans rather than aborts.
	Interruptible bool `json:"interruptible,omitempty"`

	// RunnerControl, when true, gives the main agent the runner-control
	// meta-tools (issue 1166): spawn_agent / await_agent / cancel_agent /
	// list_agents, over the configured sub-agent personas. The model can run a
	// persona in the background, get a handle, and await or cancel it. Calling
	// a persona as a tool blocks, and these tools give the model a
	// handle-based alternative. Only meaningful alongside SubAgents. False (the
	// default) omits them.
	RunnerControl bool `json:"runnerControl,omitempty"`

	// SignalPolicy selects how the main agent reacts when a signalling
	// sub-agent raises an upward Signal (issue 1165). The dispatch join reads
	// it. The values are
	//   - "" / "inject", which injects the signal as context and continues
	//     (default).
	//   - "abort_on_escalate", which ends the turn when a child raises
	//     "escalate".
	// Either way the signal is injected so the main model sees it. Only meaningful
	// alongside a sub-agent with CanSignal.
	SignalPolicy string `json:"signalPolicy,omitempty"`

	// AllowPreempt lets a sub-agent's advisory "preempt" signal actually break
	// the interruptible barrier and cancel the other in-flight sub-agents
	// (issue 1176). Without it the signal is only injected for the model to
	// weigh. False (the default) is safe. A preempt cannot cancel siblings, so
	// a rogue or prompt-injected sub-agent cannot unilaterally kill the
	// parallel work, and the main model decides on re-plan. Only meaningful
	// with Interruptible + signalling sub-agents. "escalate" always breaks the
	// barrier, whatever this is set to.
	AllowPreempt bool `json:"allowPreempt,omitempty"`

	// Servers lists the MCP servers to connect.
	Servers []ServerConfig `json:"servers"`

	// Triggers lists proactive-turn bindings over the configured event
	// streams.
	Triggers []TriggerConfig `json:"triggers,omitempty"`

	// MetaTools exposes the async control-plane tools to the model
	// (subscribe_events, create_trigger, list_tasks, cancel_task, ...).
	// Auto-implied when any server has events or triggers configured.
	MetaTools bool `json:"metaTools,omitempty"`

	// TaskGraceSec is how long a task-backed tool call runs inline before it
	// detaches to the background. Its completion then arrives as injected
	// context and a transcript line, and /tasks manages running tasks. Zero
	// uses the 10s default. Negative disables detaching, so the call waits
	// inline forever.
	TaskGraceSec int `json:"taskGraceSec,omitempty"`

	// Sampling, when enabled, lets connected MCP servers ask the session's
	// model for completions (sampling/createMessage). Each request is put to
	// the user unless the approval mode runs everything. See SamplingConfig.
	Sampling *SamplingConfig `json:"sampling,omitempty"`

	// Approval configures the tool-call permission ladder. Nil means the
	// gate is off, so every tool call the model makes runs, as before the
	// gate existed. Set it to gate calls behind a mode and per-tool rules.
	// The terminal elicitation UI shows the "ask" prompts.
	Approval *ApprovalConfig `json:"approval,omitempty"`

	// Spotlight marks untrusted tool output as data before the model reads
	// it. This mitigates indirect prompt injection, such as a fetched page
	// whose text is shaped like instructions. Nil leaves tool output
	// unmarked, as it was before this existed.
	//
	// It runs outside the approval gate, so a denied call is never marked
	// and the gate still decides on unmarked arguments.
	Spotlight *SpotlightConfig `json:"spotlight,omitempty"`

	// Critique judges each proposed tool call against stated principles
	// before it runs, and refuses the ones that violate them. Nil is off.
	//
	// Two other guardrails sit on either side of it. Spotlight marks untrusted
	// input going in, and the approval ladder gates what the user permits
	// going out. Critique adds a self-critique layer between them. It costs
	// one model call per gated call.
	Critique *CritiqueConfig `json:"critique,omitempty"`

	// Connections is a named registry of model connections with one
	// active. When set it supersedes Model for the chat provider and
	// enables runtime /provider switching. Model remains the
	// single-connection quick-start path. See ConnectionsConfig.
	Connections *ConnectionsConfig `json:"connections,omitempty"`

	// SkillDirs are local folders of skills, laid out as
	// <folder>/<name>/SKILL.md. Their skills join the skill catalog beside
	// MCP-served skills. WithSkillDirs adds more, and a surface uses it to add
	// workspace folders.
	SkillDirs []SkillDir `json:"skillDirs,omitempty"`

	// Roots lists workspace directories, and the host declares them to MCP
	// servers. A server reads them with roots/list, as file:// URIs. Empty
	// means the host advertises no roots capability, so servers see a host
	// without a workspace rather than one with an empty list. WithRoots adds
	// more, and a surface passes its workspace roots through it.
	Roots []string `json:"roots,omitempty"`

	// Offload configures tool-result offloading. Nil means off, and tool
	// results flow into the conversation verbatim. Set it to store
	// over-threshold results out of band and hand the model a compact
	// stub plus a read_tool_result tool. The backing ToolResultStore is
	// supplied by the surface via WithToolResultStore (in-memory when
	// omitted), the same split as WithRunStore.
	Offload *OffloadConfig `json:"offload,omitempty"`

	// Memory enables model-managed working memory. The model reads and
	// writes a scratchpad across turns with remember/recall/forget. Nil means
	// off.
	// The backing MemoryStore is supplied by the surface via
	// WithMemoryStore (in-memory when omitted), the same split as
	// WithRunStore and WithToolResultStore.
	Memory *MemoryConfig `json:"memory,omitempty"`

	// Compaction enables history compaction. Before each turn, if the
	// conversation exceeds MaxTokens, the chat provider summarizes the head
	// and a recent tail is kept verbatim. Nil means off, and history is sent
	// verbatim. Compaction is lossy and complements Offload, which is
	// lossless.
	Compaction *CompactionConfig `json:"compaction,omitempty"`

	// SubAgents declares specialist personas the main agent can delegate to
	// as tools (AgentSource). Each runs over the SAME provider and a filtered
	// view of the SAME server tools, with its own instructions. So each is a
	// persona, not a separately-configured agent. Empty means no sub-agents.
	SubAgents []SubAgentConfig `json:"subAgents,omitempty"`

	// FanOut declares parallel-ensemble tools (agent.FanOutSource). Each
	// group is a single tool. The main agent calls it once, the task goes to
	// all the group's member personas concurrently, and the main agent gets
	// back one aggregated result. Empty means no fan-out tools.
	FanOut []FanOutGroupConfig `json:"fanOut,omitempty"`

	// Team, when set, drives the conversation as a handoff Team (agent.Team)
	// instead of the single main agent. Control transfers between members and
	// persists across user turns. Mutually exclusive with SubAgents, FanOut, and
	// Memory (NewApp errors if combined). Nil means single-agent mode.
	Team *HostTeamConfig `json:"team,omitempty"`
}

// SubAgentConfig is one delegatable persona. The host builds it into an
// agent.AgentSource over a child Runner that shares the main provider and a
// FilterSource-narrowed view of the server tools.
type SubAgentConfig struct {
	// Name is the tool name the main agent calls to delegate. Required.
	Name string `json:"name"`

	// Description tells the main agent when to delegate to this persona.
	Description string `json:"description,omitempty"`

	// Instructions is the persona's system prompt. Empty means the sub-agent
	// is defined only by the task it is handed.
	Instructions string `json:"instructions,omitempty"`

	// Allow narrows which server tools this persona may use (by tool name).
	// Empty means all server tools.
	Allow []string `json:"allow,omitempty"`

	// MaxDepth caps sub-agent nesting for this persona. Zero uses the agent
	// default.
	MaxDepth int `json:"maxDepth,omitempty"`

	// InputSchema, when set, gives the persona a TYPED input tool instead of the
	// default {task}. The parent must pass arguments matching this JSON schema,
	// and the persona is seeded with them (structured input, 1033). Empty keeps
	// {task: string}.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`

	// ResponseSchema, when set, makes the persona return a JSON value coerced to
	// this schema instead of free text (structured output, 1033), so the
	// parent gets typed output back. Empty returns text.
	ResponseSchema json.RawMessage `json:"responseSchema,omitempty"`

	// CanSignal, when true, gives the persona the signal_parent control tool
	// (issue 1165). With it the persona can raise an upward Signal, either
	// "escalate" or "custom", to the main agent. The signal reports only the
	// persona's own state, since it has no knowledge of sibling agents. The
	// main agent reacts per Config.SignalPolicy (inject-and-continue by
	// default). False (the default) limits the persona to server tools.
	// Async personas ignore it, because a detached child has no live parent
	// dispatch to signal.
	CanSignal bool `json:"canSignal,omitempty"`

	// Async, when true, builds the persona as the spawn-and-continue Task form
	// (agent.AsyncAgentSource, 1035). The delegate tool acks immediately and
	// the child runs in the background. Its result is injected as context on a
	// later turn. Use it for a long-running subtask that the parent should not
	// block on. False (the default) is the blocking Tool form, which answers
	// this turn.
	Async bool `json:"async,omitempty"`
}

// FanOutGroupConfig declares one parallel-ensemble tool (agent.FanOutSource).
// The main agent calls Name once. The task goes to every Member persona
// concurrently, and the results are aggregated in member order. Members are
// built exactly like SubAgents (shared provider, serverTools-only, own
// instructions). They are not also exposed as individual delegate tools, so
// the group is one tool.
type FanOutGroupConfig struct {
	// Name is the tool the main agent calls to fan out. Required.
	Name string `json:"name"`

	// Description tells the main agent when to fan out to this group.
	Description string `json:"description,omitempty"`

	// Members are the personas that receive the task. At least one required.
	Members []SubAgentConfig `json:"members,omitempty"`
}

// HostTeamConfig declares a handoff Team that drives the whole conversation
// instead of the single main agent (agent.Team). Control transfers between
// members. The next user turn starts with whichever member last received
// control. Setting Team replaces the single-agent loop, so it may NOT be
// combined with SubAgents, FanOut, or Memory on the main agent (NewApp
// errors). Team members are serverTools-only personas.
type HostTeamConfig struct {
	// Members are the agents. At least one; Start must name one.
	Members []TeamMemberConfig `json:"members,omitempty"`

	// Start is the agent that receives a conversation's first turn. Required.
	Start string `json:"start"`

	// MaxHandoffs caps transfers within one user turn. Zero uses the agent
	// default.
	MaxHandoffs int `json:"maxHandoffs,omitempty"`
}

// TeamMemberConfig is one Team agent. It combines a persona with the members
// it may hand off to. The persona is a SubAgentConfig, with the shared
// provider, serverTools filtered by Allow, and its own instructions.
type TeamMemberConfig struct {
	SubAgentConfig

	// HandoffTo names the members that this agent may transfer control to.
	// Each becomes a transfer_to_<name> tool offered only to this agent. Empty
	// means a terminal agent that must answer rather than transfer.
	HandoffTo []string `json:"handoffTo,omitempty"`
}

// CompactionConfig is the host's view of history compaction; it maps to an
// agent.SummarizingCompactor over the chat provider. Its presence enables
// compaction.
type CompactionConfig struct {
	// MaxTokens is the budget (estimated) above which compaction fires.
	// Required (must be > 0); NewApp fails if it is not set.
	MaxTokens int `json:"maxTokens"`

	// KeepRecent is how many trailing messages are kept verbatim. Zero uses
	// the agent default (6).
	KeepRecent int `json:"keepRecent,omitempty"`
}

// build maps the host config onto an agent.SummarizingCompactor using
// provider (the chat model) as the summarizer.
func (c *CompactionConfig) build(provider agent.Provider) (agent.Compactor, error) {
	return agent.NewSummarizingCompactor(agent.SummarizingConfig{
		Provider:   provider,
		MaxTokens:  c.MaxTokens,
		KeepRecent: c.KeepRecent,
	})
}

// MemoryConfig is the host's view of working memory. Its presence enables
// the MemorySource; the fields tune how memory reaches the turn.
type MemoryConfig struct {
	// InjectSummary, when true, prepends a summary of the current
	// scratchpad as a RoleSystem message before each turn, so the model
	// knows what it saved without a recall call. The summary costs tokens in
	// proportion to the injected slice, so it is opt-in. When false the
	// model still reaches memory through the recall tool on demand.
	InjectSummary bool `json:"injectSummary,omitempty"`

	// SummaryMaxItems caps the number of notes in the injected summary,
	// keeping the newest. Zero means no item cap (the whole scratchpad).
	// Only meaningful with InjectSummary.
	SummaryMaxItems int `json:"summaryMaxItems,omitempty"`

	// SummaryMaxChars bounds the injected summary's rendered length (a cheap
	// token proxy), dropping the oldest kept notes until it fits. Zero means
	// no length cap. Only meaningful with InjectSummary.
	SummaryMaxChars int `json:"summaryMaxChars,omitempty"`

	// InjectRecall, when true, injects the notes RELEVANT to the current
	// user message as a RoleSystem message before each turn. The model then
	// "just knows" what matters without a recall call. This is the auto-push
	// (RAG) half of semantic recall. InjectSummary is the ambient,
	// recency-budgeted alternative, and a deployment usually picks one.
	// InjectRecall works with any backend, but it is only useful with a
	// relevance-ranking store such as a semantic store. Over the substring
	// default it injects notes that literally contain the query words.
	InjectRecall bool `json:"injectRecall,omitempty"`

	// RecallTopK caps how many relevant notes InjectRecall injects. Zero
	// uses the agent default (5).
	RecallTopK int `json:"recallTopK,omitempty"`

	// RecallMinScore drops recalled notes scoring below it. It guards against
	// poisoning. A semantic store scores every note, so when nothing truly
	// matches, low-TopK recall would still inject the least-irrelevant notes.
	// The floor stops that. Zero means no floor.
	RecallMinScore float64 `json:"recallMinScore,omitempty"`

	// SessionScoped, when true, namespaces working memory by the active run
	// id. Each session (RunStore run) gets its own scratchpad, and recall
	// never crosses sessions. On a durable backend, a /sessions resume or
	// fork keeps its own memory. It is opt-in. By default all sessions share
	// one scratchpad, and that sharing is often why memory is persistent. It
	// is a no-op without a RunStore. The run id is then always "", so every
	// turn shares the default namespace.
	SessionScoped bool `json:"sessionScoped,omitempty"`
}

// summaryOptions maps the host config onto the agent-layer budget.
func (c *MemoryConfig) summaryOptions() agent.SummaryOptions {
	return agent.SummaryOptions{MaxItems: c.SummaryMaxItems, MaxChars: c.SummaryMaxChars}
}

// recallOptions maps the host config onto the agent-layer recall budget.
func (c *MemoryConfig) recallOptions() agent.RecallOptions {
	return agent.RecallOptions{TopK: c.RecallTopK, MinScore: c.RecallMinScore}
}

// OffloadConfig is the host's view of tool-result offloading; it maps to
// an agent.OffloadConfig. Its presence enables offloading.
type OffloadConfig struct {
	// ThresholdBytes is the offload threshold. A successful result is
	// offloaded when its model-visible size is at or above it. Zero uses the
	// agent default (4 KB).
	ThresholdBytes int `json:"thresholdBytes,omitempty"`

	// PreviewLen is how many leading characters of the result the stub
	// shows inline. Zero uses the agent default.
	PreviewLen int `json:"previewLen,omitempty"`

	// PerTool overrides ThresholdBytes for named tools; a value <= 0
	// pins that tool to never offload (always inline).
	PerTool map[string]int `json:"perTool,omitempty"`
}

// toAgent maps the host config onto the agent-layer OffloadConfig.
func (c *OffloadConfig) toAgent() agent.OffloadConfig {
	return agent.OffloadConfig{
		Threshold:        c.ThresholdBytes,
		PreviewLen:       c.PreviewLen,
		PerToolThreshold: c.PerTool,
	}
}

// ApprovalConfig is the host's view of the agent approval ladder. It maps to
// an agent.TieredApproval whose "ask" outcome is presented through the same
// terminal UI as elicitation (via ElicitationCoordinator.Confirm).
type ApprovalConfig struct {
	// Mode is the default disposition, and it applies to any call that no
	// rule covers. The values are "ask" (default), "read-only-auto",
	// "reversible-auto", and "allow". "read-only-auto" auto-allows tools that
	// declare the readOnlyHint annotation and asks for the rest.
	// "reversible-auto" also auto-allows a write when the tool declares it
	// non-destructive, and asks on the rest. "allow" runs everything
	// ("yolo"). An unknown value falls back to "ask".
	Mode string `json:"mode,omitempty"`

	// Rules pins per-tool overrides that win over Mode: tool name ->
	// "allow" | "ask" | "deny". Unknown rule values are ignored.
	Rules map[string]string `json:"rules,omitempty"`

	// Remember caches a tool the user approved so later calls to it skip the
	// prompt for the life of the session.
	Remember bool `json:"remember,omitempty"`
}

// SpotlightConfig is the host's view of prompt-injection spotlighting. It maps
// to agent.Spotlight, which fences untrusted tool output in unguessable
// markers and tells the model the fenced text is data rather than
// instructions.
//
// The default is that every tool is untrusted; Tools is the per-tool opt-out.
type SpotlightConfig struct {
	// Tools labels a tool's output by where it came from: "operator"
	// (computed in-process, the only label that reaches the model unmarked),
	// "server" (relayed by a server the operator runs), "world" (fetched
	// from outside), or "agent" (produced by a sub-agent).
	//
	// The host derives a label from the tool's source, and an entry here
	// OVERRIDES it. Most deployments need no entries. A connected server's
	// tools are already "server" and a sub-agent's are already "agent". Add
	// an entry to vouch for something the host will not vouch for on its
	// own, such as an extension's tools, or a server that returns only output
	// you compute.
	//
	// List as little as possible as "operator". Suppose a tool's output did
	// not need marking. Marking it anyway costs a few tokens. Now suppose
	// you vouch for output that did need marking. That output reaches the
	// model unfenced, and the mitigation is lost. An unrecognised label is
	// treated as "world" rather than rejected, so a typo fences output
	// instead of exposing it.
	Tools map[string]string `json:"tools,omitempty"`
}

// build turns the config into the middleware, or nil when spotlighting is off.
//
// The classifier is always installed, even with no Tools entries, because
// derivation from the tool's source is the default rather than an opt-in.
// With a nil classifier everything would be labelled world. Output would
// still be marked, but every tool would get the same label, and Mark exists
// to act on the difference between labels.
func (c *SpotlightConfig) build(a *App) agent.ToolMiddleware {
	if c == nil {
		return nil
	}
	labels := make(map[string]agent.Provenance, len(c.Tools))
	for name, label := range c.Tools {
		labels[name] = parseProvenance(label)
	}
	return agent.Spotlight(agent.SpotlightConfig{Classify: a.classifyTool(labels)})
}

// parseProvenance maps a config label to a provenance, defaulting to world.
// Only an exact match on the vouched-for label can exempt a tool from
// marking, so neither an unknown value nor a casing slip can silently widen
// what reaches the model unfenced.
func parseProvenance(s string) agent.Provenance {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "operator":
		return agent.ProvenanceOperator
	case "server":
		return agent.ProvenanceServer
	case "agent":
		return agent.ProvenanceAgent
	default:
		return agent.ProvenanceWorld
	}
}

// approvalPrompt is the fallback rendering, used for any call no registered
// ApprovalRenderer claimed.
//
// The args are trimmed so a large payload does not flood the prompt. The trim
// is HERE, not in the calling path, on purpose. An unknown tool's arguments
// are an opaque blob, and trimming them is the right default. Some tools'
// arguments are the thing being reviewed, and trimming those is wrong. When
// the trim applied to everything, it cut an edit until it could not be
// reviewed. So a renderer that claims a call decides its own truncation.
func approvalPrompt(info agent.ToolCallInfo) string {
	args := strings.TrimSpace(string(info.Call.Args.Raw()))
	if len(args) > 200 {
		args = args[:200] + "…"
	}
	q := fmt.Sprintf("Allow tool call %q with %s?", info.Call.Name, args)
	if args == "" || args == "{}" {
		q = fmt.Sprintf("Allow tool call %q?", info.Call.Name)
	}
	if info.Class != nil && info.Class.Reason != "" {
		q += " (" + info.Class.Reason + ")"
	}
	return q
}

// parseApprovalMode maps a config string to an agent mode, defaulting to ask.
func parseApprovalMode(s string) agent.ApprovalMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow", "yolo", "auto", "full-auto":
		return agent.ModeAlwaysAllow
	case "read-only-auto", "readonly", "read-only":
		return agent.ModeReadOnlyAuto
	case "reversible-auto", "reversible", "auto-edit":
		return agent.ModeReversibleAuto
	default:
		return agent.ModeAlwaysAsk
	}
}

// approvalModeName is the inverse of parseApprovalMode for display.
func approvalModeName(m agent.ApprovalMode) string {
	switch m {
	case agent.ModeAlwaysAllow:
		return "allow"
	case agent.ModeReadOnlyAuto:
		return "read-only-auto"
	case agent.ModeReversibleAuto:
		return "reversible-auto"
	default:
		return "ask"
	}
}

// parseToolRule maps a config rule string to an agent rule; ok is false for an
// unrecognized value so the caller can skip it.
func parseToolRule(s string) (agent.ToolRule, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return agent.RuleAllow, true
	case "deny":
		return agent.RuleDeny, true
	case "ask":
		return agent.RuleAsk, true
	default:
		return 0, false
	}
}

// buildApproval turns the config into a live policy whose ask seam presents
// through the shared elicitation coordinator. Returns nil when no approval is
// configured (the Runner then runs every call).
func (c *ApprovalConfig) buildApproval(confirm agent.AskFunc, choose agent.ChooseFunc, key agent.RememberKeyFunc, classify agent.ClassifyFunc) *agent.TieredApproval {
	if c == nil {
		return nil
	}
	opts := []agent.TieredOption{
		agent.WithDefaultMode(parseApprovalMode(c.Mode)),
		agent.WithAsk(confirm),
		agent.WithChooser(choose),
		agent.WithRememberApprovals(c.Remember),
		agent.WithRememberKey(key),
		agent.WithClassifier(classify),
	}
	for tool, r := range c.Rules {
		if rule, ok := parseToolRule(r); ok {
			opts = append(opts, agent.WithToolRule(tool, rule))
		}
	}
	return agent.NewTieredApproval(opts...)
}

// taskGrace resolves the configured grace window.
func (c *Config) taskGrace() time.Duration {
	switch {
	case c.TaskGraceSec < 0:
		return 0
	case c.TaskGraceSec == 0:
		return client.DefaultTaskGrace
	default:
		return time.Duration(c.TaskGraceSec) * time.Second
	}
}

// ModelConfig points at an OpenAI-compatible chat-completions endpoint.
type ModelConfig struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	// APIKeyEnv names the environment variable holding the bearer key.
	// Empty means unauthenticated (local servers).
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`

	// Backup, when set, wraps the model in a FailoverProvider. If a call
	// fails cleanly on the primary, it retries here once, and the primary
	// is benched for a cooldown. See /health in the REPL.
	Backup *BackupModelConfig `json:"backup,omitempty"`
}

// BackupModelConfig is the failover endpoint (same shape as the primary,
// minus further nesting).
type BackupModelConfig struct {
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// ServerConfig is one MCP server connection.
type ServerConfig struct {
	// ID is the source identifier (used for collision-qualified tool
	// names). Must not contain underscores.
	ID string `json:"id"`

	// URL is the MCP endpoint (streamable HTTP). Set exactly one of URL and
	// Command.
	URL string `json:"url,omitempty"`

	// Command launches the server as a subprocess that speaks MCP on stdin
	// and stdout, the shape .mcp.json files use. The client's stdio transport
	// owns the process, as A12 allows. The process starts when the server
	// connects. After a crash, the next call replaces it with a fresh one. It
	// ends when the App closes. The name resolves against the agent's PATH.
	// Set exactly one of URL and Command.
	Command string `json:"command,omitempty"`

	// Args are Command's arguments.
	Args []string `json:"args,omitempty"`

	// Dir is Command's working directory, relative to the agent's own when
	// not absolute. Empty means the agent's.
	Dir string `json:"dir,omitempty"`

	// Env sets variables for Command. The child starts from an empty
	// environment rather than the agent's, so it sees only PATH and HOME
	// (copied from the agent, since launchers like npx and uvx need them),
	// the names in EnvPass, and these. A key here wins over a passed one.
	Env map[string]string `json:"env,omitempty"`

	// EnvPass names variables copied from the agent's environment into
	// Command's, for a server that needs a key the agent already holds. A name
	// the agent doesn't have is skipped.
	EnvPass []string `json:"envPass,omitempty"`

	// StartupTimeoutSec bounds how long Command gets to start and answer
	// initialize, after which the server is failed and retried in the
	// background like an unreachable URL. Zero means the client's default
	// (30 seconds for a command).
	StartupTimeoutSec int `json:"startupTimeoutSec,omitempty"`

	// CallTimeoutSec, when positive, ends any one tool call on this server
	// after that many seconds with an error. It applies to URL servers too.
	CallTimeoutSec int `json:"callTimeoutSec,omitempty"`

	// Auth configures how to authenticate to this server. Nil means
	// unauthenticated.
	Auth *AuthConfig `json:"auth,omitempty"`

	// Allow, when non-empty, restricts this server to the named tools
	// (a FilterSource capability boundary, not a display preference).
	Allow []string `json:"allow,omitempty"`

	// Required makes boot block until this server is connected. NewApp waits
	// for it, up to the required-timeout, instead of returning while it is
	// still connecting. Use it when the agent is useless without the server.
	// Non-required servers (the default) connect in the background and never
	// block boot. The agent is usable immediately, and their tools wire in as
	// they become ready. See docs/AGENT_SERVER_STATE.md.
	Required bool `json:"required,omitempty"`

	// Skills controls SEP-2640 skill loading for this server. Nil or true
	// auto-detects (servers without the capability are skipped silently);
	// false opts out even when the server advertises skills.
	Skills *bool `json:"skills,omitempty"`

	// SkillsMode selects how enabled skills enter context. "catalog" is the
	// default and shows only name + description. The load_skill tool fetches
	// a body on demand. "eager" puts the full SKILL.md bodies in the system
	// prompt. Eager fetches every skill at connect, and SEP-2640 forbids that
	// ("Hosts MUST NOT retrieve a skill's files ahead of need"). So eager is
	// an opt-in that accepts the violation, for a server you trust and a host
	// that wants the bodies up front. Ignored when Skills is false.
	//
	// Mode also controls how much a lower-trust skills server can do. Skills
	// are data, not code, and ext/skills enforces no-exec. So their risk is
	// context-poisoning. The model reads a skill body as guidance. "eager"
	// splices that text into the system prompt at connect time, with no
	// per-skill gate. "catalog" is the safer default for a server you do not
	// fully trust. A skill enters context only when the model calls
	// load_skill. load_skill is an ordinary tool, so it goes through the same
	// approval ladder as every other call. Set Approval.Rules["load_skill"] =
	// "ask" to confirm each activation, and the prompt shows the requested
	// skill name. The fetched body also goes into tool history rather than the
	// system prompt, so it has less authority. Reserve "eager" for servers you
	// trust.
	SkillsMode string `json:"skillsMode,omitempty"`

	// SkillsAllow, when non-empty, restricts this server to the named skills
	// (by skill Name), a hard capability boundary rather than a display
	// preference. The fetched index is narrowed to the allowed entries before
	// anything else, so both the injected block (eager or catalog) and the
	// load_skill tool only ever see allowed skills. Nil or empty means every
	// skill the server advertises. This is the skills analog of Allow.
	SkillsAllow []string `json:"skillsAllow,omitempty"`

	// Events lists the event streams to open on this server. Each event
	// feeds the injection policy (and any trigger bindings that match).
	Events []EventConfig `json:"events,omitempty"`

	// Agents controls experimental server-advertised agent discovery
	// (experimental/ext/agents). Nil or true auto-detects. Suppose a server
	// advertises the io.modelcontextprotocol/agents extension. The host pulls
	// its roster (agents/list) and adds each agent as a lazy delegate tool
	// that the main agent can call. A server without the capability is
	// skipped silently. False opts out even when the server advertises it.
	// Resolution waits for the first delegation. Only then does agents/get
	// fetch the instructions and scoped tools, so scoped tool schemas never
	// enter the supervisor's context. This is progressive disclosure, as with
	// catalog-mode skills. The delegated agent runs as a local child Runner,
	// and its tool calls loop back to the advertising server. It is an
	// agent.AgentSource like any other sub-agent.
	Agents *bool `json:"agents,omitempty"`
}

// EventConfig subscribes one event name and optionally overrides its
// context hint (host config wins over the server-advertised _meta hint).
type EventConfig struct {
	Name string `json:"name"`

	// Hint overrides how occurrences reach the model (priority,
	// aggregation window, template, retention, sensitivity). Without it, the
	// server's hint applies. The server declares that hint on the event type
	// in events/list, and the host re-reads it when the server announces
	// events/list_changed.
	Hint *agent.ContextHint `json:"hint,omitempty"`
}

// TriggerConfig declares one proactive-turn binding. The anti-nag policy
// mediates it. Each binding fires once, then waits for user engagement plus
// the cooldown, and a session budget applies on top.
type TriggerConfig struct {
	// Server and Event select the stream; Server empty matches any.
	Server string `json:"server,omitempty"`
	Event  string `json:"event"`

	// Filter is a set of top-level payload field equality checks, and all
	// must match. It is the config-file form of the code-level filter hook.
	// Embedders that want richer predicates use the agent package directly.
	Filter map[string]string `json:"filter,omitempty"`

	// Instructions seed the proactive turn.
	Instructions string `json:"instructions"`

	// Label names the binding in transcripts and logs.
	Label string `json:"label"`

	// CooldownSec is the re-arm floor in seconds (0 = default 300).
	CooldownSec int `json:"cooldownSec,omitempty"`
}

// AuthConfig selects one of the client auth modes MCP supports. Secrets are
// env-indirected like everything else in this config.
type AuthConfig struct {
	// Type is "bearer", "client-credentials", or "oauth". "bearer" sends a
	// static token. "client-credentials" is OAuth machine-to-machine via
	// PRM/AS discovery. "oauth" is the authorization-code browser flow. It
	// uses PRM/AS discovery + PKCE, and DCR when no client is pre-registered.
	// The /mcp overlay's login action re-runs it.
	Type string `json:"type"`

	// TokenEnv names the env var holding the static bearer token.
	// Required for type "bearer".
	TokenEnv string `json:"tokenEnv,omitempty"`

	// ClientIDEnv / ClientSecretEnv name the env vars holding the OAuth
	// client credentials. Required for type "client-credentials".
	ClientIDEnv     string `json:"clientIdEnv,omitempty"`
	ClientSecretEnv string `json:"clientSecretEnv,omitempty"`

	// Scopes to request for OAuth types. Empty inherits the server's PRM
	// scopes_supported.
	Scopes []string `json:"scopes,omitempty"`

	// AllowInsecure permits an http:// authorization server (dev/test
	// only; production AS endpoints must be https).
	AllowInsecure bool `json:"allowInsecure,omitempty"`
}

// Validate checks mode-specific requirements and that named env vars are
// actually set, so misconfiguration fails at startup rather than as a
// mid-conversation 401.
func (a *AuthConfig) Validate() error {
	switch a.Type {
	case "bearer":
		if a.TokenEnv == "" {
			return fmt.Errorf("auth type bearer requires tokenEnv")
		}
		if os.Getenv(a.TokenEnv) == "" {
			return fmt.Errorf("auth env %s is not set", a.TokenEnv)
		}
	case "client-credentials":
		if a.ClientIDEnv == "" || a.ClientSecretEnv == "" {
			return fmt.Errorf("auth type client-credentials requires clientIdEnv and clientSecretEnv")
		}
		for _, env := range []string{a.ClientIDEnv, a.ClientSecretEnv} {
			if os.Getenv(env) == "" {
				return fmt.Errorf("auth env %s is not set", env)
			}
		}
	case "oauth":
		// Authorization-code browser flow. No mandatory env: the client
		// self-registers via DCR when no clientIdEnv is given, and the PKCE
		// flow acquires interactively on the first 401. clientIdEnv (+ optional
		// clientSecretEnv) pin a pre-registered client when set.
		if a.ClientIDEnv != "" && os.Getenv(a.ClientIDEnv) == "" {
			return fmt.Errorf("auth env %s is not set", a.ClientIDEnv)
		}
	default:
		return fmt.Errorf("unknown auth type %q (want bearer, client-credentials, or oauth)", a.Type)
	}
	return nil
}

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agentchat: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agentchat: %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadConfigWithOverlay loads the base config, then merges the sibling
// local-overlay file (overlayPathFor) over it when that file exists, then
// validates. It reads what runtime-config persistence wrote. A slash command
// changes a mutable pick, such as the active connection or approval mode,
// and WithConfigOverlay writes only those picks to the overlay.
//
// The merge is deliberately a second json.Unmarshal into the same struct, so
// the semantics are the standard library's, and tests pin them. A scalar
// present in the overlay overrides the base. A map merges by key, so a base
// entry survives when the overlay omits it. A slice present in the overlay
// REPLACES the base slice wholesale. A key the overlay omits is left
// untouched. By that last rule, a one-line overlay (just active) leaves
// servers and connections intact.
func LoadConfigWithOverlay(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agentchat: parse %s: %w", path, err)
	}
	overlay := overlayPathFor(path)
	if oraw, err := os.ReadFile(overlay); err == nil {
		if err := json.Unmarshal(oraw, &cfg); err != nil {
			return nil, fmt.Errorf("agentchat: parse %s: %w", overlay, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agentchat: %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate enforces the invariants the app relies on.
func (c *Config) Validate() error {
	// A connections registry supersedes Model for the chat provider, so
	// Model is only required when no connections are configured.
	if c.Connections == nil {
		if c.Model.BaseURL == "" || c.Model.Model == "" {
			return fmt.Errorf("model.baseUrl and model.model are required (or set a connections block)")
		}
	}
	seen := map[string]bool{}
	for i, s := range c.Servers {
		if s.ID == "" {
			return fmt.Errorf("servers[%d]: id is required", i)
		}
		switch {
		case s.URL == "" && s.Command == "":
			return fmt.Errorf("servers[%d] (%s): set url or command", i, s.ID)
		case s.URL != "" && s.Command != "":
			return fmt.Errorf("servers[%d] (%s): set url or command, not both", i, s.ID)
		case s.Command != "" && s.Auth != nil:
			return fmt.Errorf("servers[%d] (%s): auth applies to a url server, not a command", i, s.ID)
		case s.URL != "" && (len(s.Args) > 0 || s.Dir != "" || len(s.Env) > 0 || len(s.EnvPass) > 0 || s.StartupTimeoutSec > 0):
			return fmt.Errorf("servers[%d] (%s): args, dir, env, envPass and startupTimeoutSec apply to a command server", i, s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("servers[%d]: duplicate id %q", i, s.ID)
		}
		seen[s.ID] = true
		if s.Auth != nil {
			if err := s.Auth.Validate(); err != nil {
				return fmt.Errorf("servers[%d] (%s): %w", i, s.ID, err)
			}
		}
	}
	return nil
}

// APIKey resolves the model bearer key from the environment. Empty when
// unset or unconfigured.
func (c *Config) APIKey() string {
	if c.Model.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.Model.APIKeyEnv)
}

// CritiqueConfig is the host's view of the constitutional critique gate. It
// maps to agent.NewCritiqueGate.
type CritiqueConfig struct {
	// Principles is the constitution, and the gate judges proposed calls
	// against it. It is required. Without principles, the gate's model call
	// has nothing to judge against, and its answer means nothing.
	Principles string `json:"principles"`

	// Tools lists the tool names to critique. Empty critiques every call,
	// which is the safe default and the expensive one, since each gated call
	// costs a model call on its latency path.
	Tools []string `json:"tools,omitempty"`

	// AllowOnError lets a call through when the critique itself fails. The
	// default (false) denies instead, because a gate that disappears when its
	// provider is down is not a gate. Set it when availability matters more
	// than the guardrail, and treat the guardrail as advisory afterwards.
	AllowOnError bool `json:"allowOnError,omitempty"`
}

// build turns the config into the middleware, or nil when critique is off.
//
// It uses the host's own provider. A cheaper or differently-aligned critic is
// a real want and is a follow-up: it needs a per-role connection lookup, and
// wiring one badly here would be worse than not offering it yet.
func (c *CritiqueConfig) build(provider agent.Provider) (agent.ToolMiddleware, error) {
	if c == nil {
		return nil, nil
	}
	cfg := agent.CritiqueConfig{
		Provider:     provider,
		Principles:   c.Principles,
		AllowOnError: c.AllowOnError,
	}
	if len(c.Tools) > 0 {
		gated := make(map[string]bool, len(c.Tools))
		for _, name := range c.Tools {
			gated[name] = true
		}
		cfg.Tools = func(name string) bool { return gated[name] }
	}
	return agent.NewCritiqueGate(cfg)
}
