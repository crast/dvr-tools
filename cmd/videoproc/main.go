package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/antonmedv/expr"
	"github.com/crast/videoproc"
	"github.com/crast/videoproc/mediainfo"
)

var configFile string
var debugMode bool

func main() {
	flag.BoolVar(&debugMode, "debug", false, "Debug Mode")
	flag.StringVar(&configFile, "config", "tmp/test.toml", "TOML config file")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Println("usage: Blah De Blah <media file>")
		flag.Usage()
		os.Exit(1)
	}

	conf, err := videoproc.ParseConfig(configFile)
	if err != nil {
		log.Fatal(err)
	}

	videoproc.MakeEvaluators(conf.Rule)

	fileName := flag.Args()[0]
	info, err := mediainfo.Parse(context.Background(), fileName)
	if err != nil {
		log.Fatal(err)
	}

	c := videoproc.EvalCtx{
		Name: filepath.Base(fileName),
	}
	for _, track := range info.Media.Tracks {
		switch v := track.Track.(type) {
		case *mediainfo.VideoTrack:
			debugPrint("video %#v", v)
			c.Height = v.Height.Int()
			c.Width = v.Width.Int()
			c.Video.Format = v.Format
			c.Video.Extra = v.Extra
			c.Video.FormatVersion = v.FormatVersion
			c.Video.FormatProfile = v.FormatProfile

		case *mediainfo.GeneralTrack:
			debugPrint("general %#v", v)
			c.Format = v.Format
			c.DurationSec = v.Duration.Float()
		case *mediainfo.AudioTrack:
			c.Audio.Format = v.Format
			c.Audio.Extra = v.Extra
		}
	}

	debugPrint("Context %#v", c)

	decision := videoproc.Rule{}

	for _, rule := range conf.Rule {
		output, err := expr.Run(rule.Evaluator, c)
		if err != nil {
			log.Fatal(err)
		}
		if !output.(bool) {
			continue
		}
		log.Printf("MATCHED RULE %v", rule.Label)
		takeString(&decision.Comskip, rule.Comskip)
		takeString(&decision.ComskipINI, rule.ComskipINI)

		decision.Actions = append(decision.Actions, rule.Actions...)

	}

}

func debugPrint(v string, args ...interface{}) {
	if debugMode {
		log.Printf(v, args...)
	}
}

func takeString(existing *string, updated string) {
	if updated != "" {
		*existing = updated
	}
}
