package videoproc

import (
	"fmt"
	"strings"

	"github.com/antonmedv/expr"
	"github.com/antonmedv/expr/vm"
	"github.com/pkg/errors"
)

func MakeEvaluators(rules []Rule) ([]Evaluator, error) {
	programs := make([]Evaluator, len(rules))
	envExpr := expr.Env(EvalCtx{})
	for i := range rules {
		rule := &rules[i]
		var e Evaluator
		if len(rule.MatchShows) != 0 {
			if len(rule.Match) != 0 {
				return nil, fmt.Errorf("rule %s: cannot have match-shows and match", rule.Label)
			}
			e = makeShowsEvaluator(rule.MatchShows)
		} else {
			prog, err := expr.Compile(rule.Match, envExpr)
			if err != nil {
				return nil, errors.Wrapf(err, "rule %s with content %+v did not compile", rule.Label, rule.Match)
			}
			e = makeProgramEvaluator(prog)
		}
		programs[i] = e
	}
	return programs, nil
}

type Evaluator func(EvalCtx) (bool, error)

func makeProgramEvaluator(program *vm.Program) Evaluator {
	return func(c EvalCtx) (bool, error) {
		value, err := expr.Run(program, c)
		if err != nil {
			return false, err
		}
		return value.(bool), nil
	}
}

func makeShowsEvaluator(shows []string) Evaluator {
	return func(c EvalCtx) (bool, error) {
		for _, show := range shows {
			if strings.HasPrefix(c.Name, show) {
				return true, nil
			}
		}
		return false, nil
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
	ScanType      string
	Extra         map[string]string
}

type AudioCtx struct {
	Format string
	Extra  map[string]string
}
