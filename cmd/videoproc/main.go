package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/antonmedv/expr"
	"github.com/crast/videoproc"
	"github.com/crast/videoproc/mediainfo"
	"github.com/sirupsen/logrus"
)

var configFile string
var debugMode bool
var scratchDir string

func main() {
	flag.BoolVar(&debugMode, "debug", false, "Debug Mode")
	flag.StringVar(&configFile, "config", "tmp/test.toml", "TOML config file")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Println("usage: Blah De Blah <media file>")
		flag.Usage()
		os.Exit(1)
	}
	if debugMode {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}

	conf, err := videoproc.ParseConfig(configFile)
	if err != nil {
		logrus.Fatal(err)
	}
	scratchDir = conf.General.ScratchDir

	evaluators := videoproc.MakeEvaluators(conf.Rule)

	fileName := flag.Args()[0]
	info, err := mediainfo.Parse(context.Background(), fileName)
	if err != nil {
		logrus.Fatal(err)
	}

	c := videoproc.EvalCtx{
		Name: filepath.Base(fileName),
	}
	for _, track := range info.Media.Tracks {
		switch v := track.Track.(type) {
		case *mediainfo.VideoTrack:
			logrus.Debugf("video %#v", v)
			c.Height = v.Height.Int()
			c.Width = v.Width.Int()
			c.Video.Format = v.Format
			c.Video.Extra = v.Extra
			c.Video.FormatVersion = v.FormatVersion
			c.Video.FormatProfile = v.FormatProfile

		case *mediainfo.GeneralTrack:
			logrus.Debugf("general %#v", v)
			c.Format = v.Format
			c.DurationSec = v.Duration.Float()

		case *mediainfo.AudioTrack:
			c.Audio.Format = v.Format
			c.Audio.Extra = v.Extra
		}
	}

	logrus.Debugf("Context %#v", c)

	decision := &videoproc.Rule{}

	for i, rule := range conf.Rule {
		output, err := expr.Run(evaluators[i], c)
		if err != nil {
			logrus.Fatal(err)
		}
		if !output.(bool) {
			continue
		}
		logrus.Infof("MATCHED RULE %v", rule.Label)

		takeString(&decision.Comskip, rule.Comskip)
		takeString(&decision.ComskipINI, rule.ComskipINI)
		decision.Actions = append(decision.Actions, rule.Actions...)
	}

	logrus.Debugf("About to execute: %#v", decision)

	var ffmpegOpts []string

	if decision.Comskip == "chapter" || decision.Comskip == "comchap" || isTrue(decision.Comskip) {
		commercials := runComskip(fileName, decision)
		if len(commercials) != 0 {
			var buf bytes.Buffer
			buf.WriteString(";FFMETADATA1\n")
			begin := float64(0.0)
			for i, commercial := range commercials {
				if i != 0 || commercial.Begin > 0.5 {
					writeChapter(&buf, begin, commercial.Begin, fmt.Sprintf("Segment %v", i+1))
				}
				writeChapter(&buf, commercial.Begin, commercial.End, fmt.Sprintf("Commercial %v", i+1))

				begin = commercial.End
			}

			if lastComm := commercials[len(commercials)-1]; lastComm.End < (c.DurationSec - 0.3) {
				writeChapter(&buf, lastComm.End, c.DurationSec, fmt.Sprintf("Segment %d", len(commercials)+1))
			}
			logrus.Info(buf.String())

			metaFile := filepath.Join(scratchDir, filepath.Base(fileName)+".ffmeta")
			if err := ioutil.WriteFile(metaFile, buf.Bytes(), 0666); err != nil {
				logrus.Fatal(err)
			}
			ffmpegOpts = append(ffmpegOpts, "-i", metaFile, "-map_metadata", "1")
		}
	}

	for _, action := range decision.Actions {
		switch action {
		case "force-anamorphic":
			ffmpegOpts = append(ffmpegOpts, "-aspect", "16:9")
		default:
			logrus.Fatalf("unrecognized action %s", action)
		}
	}

	baseCmd := append([]string{
		"-nostdin", "-i", fileName,
	}, ffmpegOpts...)

	tmpOutFile := filepath.Join(scratchDir, filepath.Base(fileName)+".mkv")
	baseCmd = append(baseCmd, "-c:v", "copy", "-c:a", "copy", "-c:s", "copy", tmpOutFile)
	logrus.Debugf("About to ffmpeg %#v", baseCmd)

	cmd := exec.Command("ffmpeg", baseCmd...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Run()
}

func writeChapter(buf *bytes.Buffer, begin, end float64, title string) {
	fmt.Fprintf(buf, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", int(begin*1000.0), int(end*1000.0), title)
}

func runComskip(fileName string, decision *videoproc.Rule) []Commercial {
	logrus.Infof("About to run comskip")
	csPrefix := "vp" + strconv.Itoa(os.Getpid())
	cmd := exec.Command(
		"comskip",
		"--demux",
		"--ini="+decision.ComskipINI,
		"--output="+scratchDir,
		"--output-filename="+csPrefix,
		"--verbose=1",
		fileName,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logrus.Debug(cmd.Args)
	if err := cmd.Run(); err != nil {
		logrus.Fatal(err)
	}

	f, _ := os.Open(filepath.Join(scratchDir, csPrefix+".edl"))
	defer f.Close()
	reader := csv.NewReader(f)
	reader.Comma = '\t'
	edits, _ := reader.ReadAll()

	logrus.Warnf("edits: %#v", edits)
	var commercials []Commercial
	for _, edit := range edits {
		begin, _ := strconv.ParseFloat(edit[0], 64)
		end, _ := strconv.ParseFloat(edit[1], 64)
		commercials = append(commercials, Commercial{begin, end})
	}
	return commercials
}

type Commercial struct {
	Begin float64
	End   float64
}

func takeString(existing *string, updated string) {
	if updated != "" {
		*existing = updated
	}
}

func isFalse(v string) bool {
	v = strings.ToLower(v)
	return v == "" || v == "false" || v == "no" || v == "disable"
}

func isTrue(v string) bool {
	v = strings.ToLower(v)
	return v == "true" || v == "yes"
}
