package videoproc

import (
	"log"

	"github.com/antonmedv/expr"
	"github.com/antonmedv/expr/vm"
)

func MakeEvaluators(rules []Rule) []*vm.Program {
	programs := make([]*vm.Program, len(rules))
	envExpr := expr.Env(EvalCtx{})
	for i := range rules {
		rule := &rules[i]
		prog, err := expr.Compile(rule.Match, envExpr)
		if err != nil {
			log.Fatal(err)
		}
		programs[i] = prog
	}
	return programs
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
