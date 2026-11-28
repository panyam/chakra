package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// planTools adds the current case's write_slot tool to base. Each case
// gets fresh notes in its Setup, so the tool looks them up on every call.
type planTools struct {
	base agent.ToolSource
	st   *state
}

func (p *planTools) Tools(ctx context.Context) ([]core.ToolDef, error) {
	defs, err := p.base.Tools(ctx)
	if err != nil {
		return nil, err
	}
	if slots := p.st.get(); slots != nil {
		more, err := slots.Tool("plan").Tools(ctx)
		if err != nil {
			return nil, err
		}
		defs = append(defs, more...)
	}
	return defs, nil
}

func (p *planTools) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if slots := p.st.get(); name == agent.SlotToolName && slots != nil {
		return slots.Tool("plan").Call(ctx, name, args)
	}
	return p.base.Call(ctx, name, args)
}

// childInstructions is a sub-agent's prompt, for one source.
const childInstructions = `You research one task in the %s pages, with the search and fetch tools.
Report every fact that bears on the task, each with the URL of the page you fetched it from. Report only what the pages say. If they say nothing on the task, say so.`

// subAgentRules is what the sub-agents step adds to the parent's prompt.
const subAgentRules = `The research tool sends a task to three researchers at once, one for each source: wikipedia, wikivoyage and archive. Each reports facts with URLs. Send it one sub-question at a time. The archive holds older revisions of Wikipedia pages, so prefer a newer page when two disagree. Fetch a page yourself when you need to check it.`

// withSubAgents replaces search with the research tool, which fans a task
// out to one sub-agent per source. The parent keeps fetch, to check a page
// itself, and its notes.
func withSubAgents(cfg agent.RunnerConfig, c *corpus, st *state) (agent.RunnerConfig, error) {
	var members []*agent.AgentSource
	for _, src := range sources {
		tools, err := c.tools(src)
		if err != nil {
			return cfg, err
		}
		child, err := agent.NewRunner(agent.RunnerConfig{
			Provider:     st.meter(cfg.Provider),
			Tools:        tools,
			Instructions: fmt.Sprintf(childInstructions, src),
			MaxSteps:     8,
		})
		if err != nil {
			return cfg, err
		}
		m, err := agent.NewAgentSource(agent.AgentSourceConfig{Name: src, Description: "Research a task in the " + src + " pages.", Runner: child})
		if err != nil {
			return cfg, err
		}
		members = append(members, m)
	}
	fan, err := agent.NewFanOutSource(agent.FanOutConfig{
		Name:        "research",
		Description: "Send one research task to a researcher for each source, wikipedia, wikivoyage and archive, at once. Each reports facts with the URLs of the pages they came from.",
		Members:     members,
	})
	if err != nil {
		return cfg, err
	}
	all, err := c.tools("")
	if err != nil {
		return cfg, err
	}
	m := agent.NewMultiSource()
	if err := m.Add("research", fan); err != nil {
		return cfg, err
	}
	if err := m.Add("fetch", onlyTool{all, "fetch"}); err != nil {
		return cfg, err
	}
	cfg.Tools = &planTools{base: m, st: st}
	cfg.Instructions += "\n" + subAgentRules
	return cfg, nil
}

// onlyTool is one tool of a source.
type onlyTool struct {
	src  agent.ToolSource
	name string
}

func (o onlyTool) Tools(ctx context.Context) ([]core.ToolDef, error) {
	defs, err := o.src.Tools(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		if d.Name == o.name {
			return []core.ToolDef{d}, nil
		}
	}
	return nil, nil
}

func (o onlyTool) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name != o.name {
		return nil, fmt.Errorf("%w: %q", agent.ErrUnknownTool, name)
	}
	return o.src.Call(ctx, name, args)
}

// criticPrompt asks a second model whether the cited pages support the
// answer.
const criticPrompt = `You check a researcher's answer against the pages it cites. Reply with SUPPORTED if the pages state the answer to the question, and the answer doesn't rely on an older page that a newer one contradicts. Otherwise reply UNSUPPORTED, then one sentence saying what is wrong.

Question: %s

Answer: %s

%s`

// critic refuses an answer whose cited pages don't support it, on a second
// call to the same model. It reads the cited pages from the corpus, cut to
// 20000 characters each, and judges only answers that cite pages. On the
// last step it accepts what it gets.
func critic(p agent.Provider, c *corpus) agent.TurnEndHook {
	return func(ctx context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		if info.StepsLeft <= 1 {
			return agent.TurnEndVerdict{}, nil
		}
		var pages strings.Builder
		for _, u := range cited(info.Text) {
			pg := c.page(u)
			if pg == nil {
				continue
			}
			fmt.Fprintf(&pages, "Page %s (%s):\n%s\n\n", pg.url, pg.title, pg.text[:min(len(pg.text), 20000)])
		}
		if pages.Len() == 0 {
			return agent.TurnEndVerdict{}, nil
		}
		question := ""
		for _, m := range info.Messages {
			if m.Role == agent.RoleUser {
				question = m.Text
				break
			}
		}
		resp, err := p.Generate(ctx, agent.ProviderRequest{Messages: []agent.Message{
			{Role: agent.RoleUser, Text: fmt.Sprintf(criticPrompt, question, answerText(info.Text), pages.String())},
		}})
		if err != nil {
			return agent.TurnEndVerdict{}, nil
		}
		verdict := strings.TrimSpace(resp.Text)
		if strings.HasPrefix(strings.ToUpper(verdict), "UNSUPPORTED") {
			return agent.TurnEndVerdict{Continue: "A reviewer read the pages you cite and says: " + verdict + " Research it again, then answer."}, nil
		}
		return agent.TurnEndVerdict{}, nil
	}
}

// costCap is the most tokens one question may spend, its sub-agents
// included.
const costCap = 300000
