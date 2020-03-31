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
	General GeneralConfig
	Rule    []Rule
}

type Rule struct {
	Label      string
	Match      string
	Comskip    string
	ComskipINI string `toml:"comskip-ini"`
	Actions    []string
}

type GeneralConfig struct {
	ScratchDir string `toml:"scratch-dir"`
}
