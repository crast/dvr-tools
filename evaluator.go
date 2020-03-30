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
	Format      string
	DurationSec float64

	Tags []string
}
