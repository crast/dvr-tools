package videoproc

import (
	"log"

	"github.com/antonmedv/expr"
)

func MakeEvaluators(rules []Rule) {
	envExpr := expr.Env(EvalCtx{})
	for i := range rules {
		rule := &rules[i]
		prog, err := expr.Compile(rule.Match, envExpr)
		if err != nil {
			log.Fatal(err)
		}
		rule.Evaluator = prog
	}
}

type EvalCtx struct {
	Name string

	Width       int
	Height      int
	DurationSec float64
	Format      string

	Audio AudioCtx
	Video VideoCtx

	Tags []string
}

type VideoCtx struct {
	Format        string
	FormatVersion string
	FormatProfile string
	Extra         map[string]string
}

type AudioCtx struct {
	Format string
	Extra  map[string]string
}
