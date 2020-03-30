package main

import (
	"log"

	"github.com/crast/videoproc"
)

func main() {
	conf, err := videoproc.ParseConfig("tmp/test.toml")
	if err != nil {
		log.Fatal(err)
	}

	videoproc.MakeEvaluators(conf.Rule)
}
