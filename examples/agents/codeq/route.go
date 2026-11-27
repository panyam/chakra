package codeq

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/panyam/chakra"
)

// routes is the note the routing step sends with each kind of question.
var routes = map[string]string{
	definition: "This question asks where a name is declared. Call find with the bare name. If find returns more than one row, keep only the row in the module the question names.",
	imports:    "This question asks what one package imports, or what a module holds. One call of imports or packages answers it.",
	reverseHop: "This question asks what imports a package. One call of importers answers it. Then apply the question's scoping rules.",
	negation:   "This question asks which packages import nothing from somewhere. Call packages for the module, then imports for each of its packages, and answer with the packages that import nothing from the other place.",
	callers:    "This question asks what calls a function. Call callers with the function's qualified name. If you only have a bare name, call find first.",
	multiHop:   `This question needs more than one lookup, all with the same tool: callers for what calls a function, imports for what a package depends on, importers for what depends on a package. Make the lookup on the question's function or package. Then make it again on every new row in one step, calling the tool once per row, and repeat until no new row comes back. Walk only rows that start with example.com/, from both modules, since a path can pass through the other module. Leave rows out only in the answer: rows from other modules when the question says "in <module>", and the first lookup's rows when it says "only through". When it says "non-test", don't walk rows marked (test files only).`,
}

var (
	routeMultiHop   = regexp.MustCompile(`directly or through|only through`)
	routeNegation   = regexp.MustCompile(`import nothing from`)
	routeDefinition = regexp.MustCompile(`^What is the qualified name`)
	routeCallers    = regexp.MustCompile(`^Which functions `)
	routeImports    = regexp.MustCompile(`^Which packages (does \S+ import|make up)`)
)

// route names the kind of lookup a question needs from its wording, with
// the same tags the questions carry. It knows the eval's phrasings, as a
// router built for one app knows that app's questions.
func route(question string) string {
	switch {
	case routeMultiHop.MatchString(question):
		return multiHop
	case routeNegation.MatchString(question):
		return negation
	case routeDefinition.MatchString(question):
		return definition
	case routeCallers.MatchString(question):
		return callers
	case routeImports.MatchString(question):
		return imports
	default:
		return reverseHop
	}
}

// router is the routing step's stage. Before each model call it reads the
// turn's question, routes it, and adds that route's note for this call
// only, so the note never joins the record.
func router() agent.StepStage {
	return agent.StepStage{
		Name:  "codeq.route",
		Scope: agent.ScopeRequest,
		Run: func(_ context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			for _, m := range v.Messages {
				if m.Role != agent.RoleUser {
					continue
				}
				note := routes[route(strings.TrimSpace(m.Text))]
				v.Messages = append(slices.Clip(v.Messages), agent.Message{Role: agent.RoleSystem, Text: note})
				break
			}
			return v, nil
		},
	}
}
