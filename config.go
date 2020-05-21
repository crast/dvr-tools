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
	Profile []EncodeConfig
	Rule    []Rule
}

type Rule struct {
	Label      string
	Match      string
	Comskip    string
	ComskipINI string `toml:"comskip-ini"`
	Actions    []string

	Profile string
	Encode  EncodeConfig
}

type GeneralConfig struct {
	ScratchDir  string `toml:"scratch-dir"`
	WatchLogDir string `toml:"watch-log-dir"`
	RoundCuts   bool   `toml:"round-cuts"`
}

type EncodeConfig struct {
	Name        string
	Deinterlace bool
	Video       EncodeVideo
	Audio       EncodeAudio
}
type EncodeVideo struct {
	Codec  string
	Preset string
	CRF    string
	Level  string
	Crop   string
}

type EncodeAudio struct {
	Codec   string
	Bitrate string
}
