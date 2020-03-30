package videoproc

import (
	"github.com/BurntSushi/toml"
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
	Match string
}
