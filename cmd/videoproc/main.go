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
var deleteOriginal bool

func main() {
	flag.BoolVar(&debugMode, "debug", false, "Debug Mode")
	flag.StringVar(&configFile, "config", "tmp/test.toml", "TOML config file")
	flag.BoolVar(&deleteOriginal, "delete-orig", false, "Delete original file")
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
		//commercials := edlToCommercials("/loc/scratch/vp57433.edl")
		if len(commercials) != 0 {
			chapters := makeChapters(commercials, c.DurationSec)

			if strings.HasSuffix(fileName, ".mkv") {
				logrus.Warn("Swapping properties using mkvpropedit")
				if err := editMKVChapters(fileName, chapters); err != nil {
					logrus.Fatal(err)
				}
				return
			} else {
				buf := chaptersToFF(chapters)
				logrus.Info(string(buf))

				metaFile := filepath.Join(scratchDir, filepath.Base(fileName)+".ffmeta")
				if err := ioutil.WriteFile(metaFile, buf, 0666); err != nil {
					logrus.Fatal(err)
				}
				ffmpegOpts = append(ffmpegOpts, "-i", metaFile, "-map_metadata", "1")
			}
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

	destFile := stripExtension(fileName) + ".mkv"

	tmpOutFile := filepath.Join(scratchDir, filepath.Base(destFile))
	baseCmd = append(baseCmd, "-c", "copy", tmpOutFile)
	logrus.Debugf("About to ffmpeg %#v", baseCmd)

	cmd := exec.Command("ffmpeg", baseCmd...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		logrus.Fatal(err)
	}

	if err := os.Rename(tmpOutFile, destFile); err != nil {
		if err := runCommand("mv", "--", tmpOutFile, destFile); err != nil {
			logrus.Fatal(err)
		}
	}
	if deleteOriginal && fileName != destFile {
		if err := os.Remove(fileName); err != nil {
			logrus.Fatal(err)
		}
	}
}

func editMKVChapters(fileName string, chapters []Chapter) error {
	var buf bytes.Buffer
	for i, chapter := range chapters {
		cnum := fmt.Sprintf("%02d", i+1)
		fmt.Fprintf(&buf, "CHAPTER%s=%s\nCHAPTER%sNAME=%s\n", cnum, timestampMKV(chapter.Begin), cnum, chapter.Name)
	}
	logrus.Debug("chapterfile", buf.String())
	chapterFile := filepath.Join(scratchDir, strings.Replace(filepath.Base(fileName), ".mkv", ".chapter", -1))
	if err := ioutil.WriteFile(chapterFile, buf.Bytes(), 0666); err != nil {
		return err
	}

	return runCommand("mkvpropedit", fileName, "--chapters", chapterFile)
}

func timestampMKV(floatSeconds float64) string {
	rawSeconds := int(floatSeconds)
	seconds := rawSeconds % 60
	rawMinutes := rawSeconds / 60
	hours := rawMinutes / 60

	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, rawMinutes%60, seconds, int((floatSeconds-float64(rawSeconds))*1000.0))
}

func makeChapters(commercials []Commercial, fileDuration float64) []Chapter {
	var chapters []Chapter

	begin := float64(0.0)
	for i, commercial := range commercials {
		if i != 0 || commercial.Begin > 0.5 {
			chapters = append(chapters, Chapter{
				Begin: begin, End: commercial.Begin,
				Name: fmt.Sprintf("Segment %v", i+1),
			})
		}

		chapters = append(chapters, Chapter{
			Begin: commercial.Begin, End: commercial.End,
			Name:         fmt.Sprintf("Commercial %v", i+1),
			IsCommercial: true,
		})

		begin = commercial.End
	}
	if lastComm := commercials[len(commercials)-1]; lastComm.End < (fileDuration - 0.3) {
		chapters = append(chapters, Chapter{
			Begin: lastComm.End, End: fileDuration,
			Name: fmt.Sprintf("Segment %v", len(commercials)+1),
		})
	}
	return chapters
}

func chaptersToFF(chapters []Chapter) []byte {
	var buf bytes.Buffer
	buf.WriteString(";FFMETADATA1\n")
	for _, chapter := range chapters {
		fmt.Fprintf(&buf, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", int(chapter.Begin*1000.0), int(chapter.End*1000.0), chapter.Name)
	}
	return buf.Bytes()
}

func runComskip(fileName string, decision *videoproc.Rule) []Commercial {
	logrus.Infof("About to run comskip")
	csPrefix := "vp" + strconv.Itoa(os.Getpid())
	cmd := exec.Command(
		"comskip",
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
	return edlToCommercials(filepath.Join(scratchDir, csPrefix+".edl"))
}

func edlToCommercials(edlFile string) []Commercial {
	f, _ := os.Open(edlFile)
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

type Chapter struct {
	Begin        float64
	End          float64
	Name         string
	IsCommercial bool
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

func stripExtension(fileName string) string {
	i := strings.LastIndex(fileName, ".")
	return fileName[:i]
}

func runCommand(prog string, args ...string) error {
	cmd := exec.Command(prog, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
