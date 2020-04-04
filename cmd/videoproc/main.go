package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crast/videoproc"
	"github.com/crast/videoproc/mediainfo"

	"github.com/antonmedv/expr"
	"github.com/nightlyone/lockfile"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const FLAG_VER = "2"

var configFile string
var debugMode bool
var scratchDir string
var deleteOriginal bool

func main() {
	defaultConfigFile := "tmp/test.toml"
	if len(os.Args) >= 1 {
		tomlFile := os.Args[0] + ".toml"
		if info, err := os.Stat(tomlFile); err == nil && info.Size() > 0 {
			defaultConfigFile = tomlFile
		}
	}
	var lockFile string
	flag.BoolVar(&debugMode, "debug", false, "Debug Mode")
	flag.StringVar(&configFile, "config", defaultConfigFile, "TOML config file")
	flag.BoolVar(&deleteOriginal, "delete-orig", false, "Delete original file")
	flag.StringVar(&lockFile, "lock-file", "", "Lock on this file")
	flag.Parse()
	if debugMode {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}

	logrus.Debugf("videoproc FLAG %s", FLAG_VER)

	if flag.NArg() != 1 {
		fmt.Println("usage: Blah De Blah <media file>")
		flag.Usage()
		os.Exit(1)
	}

	conf, err := videoproc.ParseConfig(configFile)
	if err != nil {
		logrus.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, os.Interrupt, syscall.SIGQUIT)
		for _ = range ch {
			cancel()
		}
	}()

	if lockFile != "" {
		l, err := lockfile.New(lockFile)
		if err != nil {
			logrus.Fatal(err)
			return
		}
		for {
			if err := l.TryLock(); err == nil {
				defer l.Unlock()
				break
			}
			logrus.Debug("Lockfile was held, sleeping 1 sec")
			time.Sleep(1 * time.Second)
		}
	}

	fileName := flag.Args()[0]

	job := &Job{
		Config: conf,
	}
	if err := processVideo(ctx, job, fileName); err != nil {
		job.DeleteErroredFiles()
		logrus.Fatal(err)
	} else {
		job.DeleteFiles()
	}
}

func processVideo(ctx context.Context, job *Job, fileName string) error {
	scratchDir = job.Config.General.ScratchDir
	evaluators := videoproc.MakeEvaluators(job.Config.Rule)

	info, err := mediainfo.Parse(context.Background(), fileName)
	if err != nil {
		return errors.Wrap(err, "could not parse mediainfo")
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
			c.Video.ScanType = v.ScanType

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

	for i, rule := range job.Config.Rule {
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
		takeString(&decision.Profile, rule.Profile)
		decision.Actions = append(decision.Actions, rule.Actions...)
		copyEncodeRule(&decision.Encode, rule.Encode)
	}

	if decision.Profile != "" {
		for _, profile := range job.Config.Profile {
			if profile.Name == decision.Profile {
				cloned := profile
				copyEncodeRule(&cloned, decision.Encode)
				decision.Encode = cloned
			}
		}
	}

	logrus.Debugf("About to execute: %#v", decision)

	var ffmpegOpts []string

	if decision.Comskip == "chapter" || decision.Comskip == "comchap" || isTrue(decision.Comskip) {
		commercials, err := runComskip(ctx, job, fileName, decision)
		if err != nil {
			return errors.Wrap(err, "could not run comskip")
		}
		//commercials := edlToCommercials("/loc/scratch/vp57433.edl")
		if len(commercials) != 0 {
			chapters := makeChapters(commercials, c.DurationSec)

			if strings.HasSuffix(fileName, ".mkv") {
				logrus.Warn("Swapping properties using mkvpropedit")
				if err := editMKVChapters(ctx, job, fileName, chapters); err != nil {
					return errors.Wrap(err, "Could not edit MKV chapters")
				}
				return nil
			} else {
				buf := chaptersToFF(chapters)
				logrus.Info(string(buf))

				metaFile := filepath.Join(scratchDir, filepath.Base(fileName)+".ffmeta")
				job.TrackFile(metaFile, false)
				if err := ioutil.WriteFile(metaFile, buf, 0666); err != nil {
					return err
				}
				ffmpegOpts = append(ffmpegOpts, "-i", metaFile, "-map_metadata", "1")
			}
		}
	}

	if len(decision.Actions) == 0 && len(ffmpegOpts) == 0 {
		logrus.Debug("No actions determined, exiting")
		return nil
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
	baseCmd = append(baseCmd, "-metadata", "videoproc="+FLAG_VER)

	destFile := stripExtension(fileName) + ".mkv"

	tmpOutFile := filepath.Join(scratchDir, filepath.Base(destFile))
	baseCmd = append(baseCmd, "-c", "copy", tmpOutFile)
	logrus.Debugf("About to ffmpeg %#v", baseCmd)

	if err := runCommand(ctx, "ffmpeg", baseCmd...); err != nil {
		logrus.Fatal(err)
	}

	if err := os.Rename(tmpOutFile, destFile); err != nil {
		logrus.Infof("got error with naive rename %s; going to use 'mv' instead", err.Error())
		if err := runCommand(ctx, "mv", "--", tmpOutFile, destFile); err != nil {
			return errors.Wrap(err, "could not run mv")
		}
	}
	if deleteOriginal && fileName != destFile {
		if err := os.Remove(fileName); err != nil {
			return errors.Wrap(err, "could not remove orig")
		}
	}
	return nil
}

func copyEncodeRule(dst *videoproc.EncodeConfig, src videoproc.EncodeConfig) {
	takeString(&dst.Video.Codec, src.Video.Codec)
	takeString(&dst.Video.Preset, src.Video.Preset)
	takeString(&dst.Video.CRF, src.Video.CRF)
	takeString(&dst.Video.Level, src.Video.Level)
	takeString(&dst.Audio.Codec, src.Audio.Codec)
	takeString(&dst.Audio.Bitrate, src.Audio.Bitrate)
}

type TrackedFile struct {
	Filename  string
	MissingOK bool
}

type Job struct {
	Config       *videoproc.Config
	filesTracked []TrackedFile
}

func (job *Job) TrackFile(fileName string, missingOK bool) {
	job.filesTracked = append(job.filesTracked, TrackedFile{fileName, missingOK})
}
func (job *Job) DeleteErroredFiles() {
	for _, entry := range job.filesTracked {
		os.Remove(entry.Filename)
	}
}

func (job *Job) DeleteFiles() {
	for _, entry := range job.filesTracked {
		_, err := os.Stat(entry.Filename)
		if err == nil {
			logrus.Debugf("Deleting %s", entry.Filename)
			os.Remove(entry.Filename)
		} else {
			if os.IsNotExist(err) {
				if !entry.MissingOK {
					logrus.Warnf("Expected to delete %s but it was missing", entry.Filename)
				}
			} else {
				logrus.Warnf("Got error %s deleting %s", err.Error(), entry.Filename)
			}
		}
	}
}

func editMKVChapters(ctx context.Context, job *Job, fileName string, chapters []Chapter) error {
	var buf bytes.Buffer
	for i, chapter := range chapters {
		cnum := fmt.Sprintf("%02d", i+1)
		fmt.Fprintf(&buf, "CHAPTER%s=%s\nCHAPTER%sNAME=%s\n", cnum, timestampMKV(chapter.Begin), cnum, chapter.Name)
	}
	logrus.Debug("chapterfile", buf.String())
	chapterFile := filepath.Join(scratchDir, strings.Replace(filepath.Base(fileName), ".mkv", ".chapter", -1))
	err := ioutil.WriteFile(chapterFile, buf.Bytes(), 0666)
	job.TrackFile(chapterFile, (err != nil))
	if err != nil {
		return err
	}

	return runCommand(ctx, "mkvpropedit", fileName, "--chapters", chapterFile)
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

func runComskip(ctx context.Context, job *Job, fileName string, decision *videoproc.Rule) ([]Commercial, error) {
	logrus.Infof("About to run comskip")
	csPrefix := "vp" + strconv.Itoa(os.Getpid())
	cmd := exec.CommandContext(
		ctx,
		"comskip",
		"--ini="+decision.ComskipINI,
		"--output="+scratchDir,
		"--output-filename="+csPrefix,
		"--verbose=1",
		fileName,
	)
	sbuf := &stdbuf{Name: "stdout"}
	cmd.Stdout = sbuf
	cmd.Stderr = os.Stderr

	logrus.Debug(cmd.Args)
	absoluteBase := filepath.Join(scratchDir, csPrefix)
	job.TrackFile(absoluteBase+".ccyes", true)
	job.TrackFile(absoluteBase+".edl", true)
	job.TrackFile(absoluteBase+".txt", true)
	job.TrackFile(absoluteBase+".log", true)
	if err := cmd.Run(); err != nil {
		scanner := bufio.NewScanner(&sbuf.buf)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "Commercials were not found." {
				logrus.Debug("Detected failure in comskip but got comm not found, moving on.")
				return nil, nil
			}
		}

		return nil, errors.Wrap(err, "could not run comskip")
	}

	return edlToCommercials(absoluteBase + ".edl")
}

type stdbuf struct {
	Name string
	mu   sync.Mutex
	buf  bytes.Buffer
}

func (b *stdbuf) Write(v []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	logrus.Debugf("%s: %s", b.Name, string(v))
	return b.buf.Write(v)
}

func edlToCommercials(edlFile string) ([]Commercial, error) {
	f, err := os.Open(edlFile)
	if err != nil {
		return nil, errors.Wrap(err, "could not open EDL")
	}
	defer f.Close()
	reader := csv.NewReader(f)
	reader.Comma = '\t'
	edits, err := reader.ReadAll()
	if err != nil {
		return nil, errors.Wrap(err, "could not parse CSV")
	}

	logrus.Warnf("edits: %#v", edits)
	var commercials []Commercial
	for _, edit := range edits {
		begin, _ := strconv.ParseFloat(edit[0], 64)
		end, _ := strconv.ParseFloat(edit[1], 64)
		commercials = append(commercials, Commercial{begin, end})
	}
	return commercials, nil

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

func runCommand(ctx context.Context, prog string, args ...string) error {
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
