package videoproc

import (
	"log"

	"github.com/antonmedv/expr"
)

func MakeEvaluators(rules []Rule) {
	for _, rule := range rules {
		_, err := expr.Compile(rule.Match, expr.Env(EvalCtx{}))
		if err != nil {
			log.Fatal(err)
		}
	}
}

type EvalCtx struct {
	Name string

	Width  uint
	Height uint
}
