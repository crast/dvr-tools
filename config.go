package videoproc

import (
	"github.com/BurntSushi/toml"
	"github.com/antonmedv/expr/vm"
)

func ParseConfig(filename string) (*Config, error) {
	var conf Config
	_, err := toml.DecodeFile(filename, &conf)
	return &conf, err
}

type Config struct {
	Rule []Rule
}

type Rule struct {
	Label      string
	Match      string
	Comskip    string
	ComskipINI string `toml:"comskip-ini"`
	Actions    []string
	Evaluator  *vm.Program `toml:"-"`
}
