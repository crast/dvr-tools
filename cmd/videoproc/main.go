package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
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
	"github.com/crast/videoproc/internal/fileio"
	"github.com/crast/videoproc/mediainfo"
	"github.com/crast/videoproc/watchlog"

	"github.com/antonmedv/expr"
	"github.com/nightlyone/lockfile"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const FLAG_VER = "3"

var configFile string
var debugMode bool
var scratchDir string
var deleteOriginal bool
var useExistingChapters bool

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
	flag.BoolVar(&useExistingChapters, "existing-chapters", false, "Use existing chapters if possible")
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
	isMKV := false
	hasChapters := false
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
			if v.Format == "Matroska" {
				isMKV = true
			}
			c.Format = v.Format
			c.DurationSec = v.Duration.Float()

		case *mediainfo.AudioTrack:
			c.Audio.Format = v.Format
			c.Audio.Extra = v.Extra
		case *mediainfo.MenuTrack:
			hasChapters = true
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

	hasMKVChapters := isMKV && hasChapters

	var ffmpegOpts []string

	if decision.Comskip == "chapter" || decision.Comskip == "comchap" || isTrue(decision.Comskip) {
		var chapters []Chapter
		if useExistingChapters {
			logrus.Warn("extract chapters from existing")
			var wlchapters []Chapter
			var wl *watchlog.WatchLog
			if wlDir := job.Config.General.WatchLogDir; wlDir != "" {
				logrus.Debug("check watchlog")
				wl, err = getWatchLogIfExists(ctx, wlDir, fileName)
				if err != nil {
					return err
				} else if wl != nil {
					logrus.Info("got watchlog")
					wlchapters = watchLogChapters(wl)
					if wl.Note == "prefer" {
						logrus.Info("Preferring watchlog chapters")
						chapters = wlchapters
					}
				}
			}

			if wl == nil || wl.Note != "prefer" {
				chapters, err = extractExistingChapters(ctx, job, fileName)
				if err != nil {
					return err
				}
				if len(chapters) != 0 {
					hasMKVChapters = true
				}

			}

			if len(chapters) == 0 && len(wlchapters) != 0 {
				chapters = wlchapters
			}

		} else {
			commercials, err := runComskip(ctx, job, fileName, decision)
			if err != nil {
				return errors.Wrap(err, "could not run comskip")
			}
			if len(commercials) != 0 {
				chapters = makeChapters(commercials, c.DurationSec)
			}

		}
		if len(chapters) != 0 {

			if decision.Comskip == "chapter" || decision.Comskip == "comchap" {
				if isMKV {
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
			} else {
				extraArgs, _ := ffmpegExtractFilters(ctx, job, fileName, nonCommercialChapters(chapters))
				logrus.Warn("Extra Filters %+v", extraArgs)
				ffmpegOpts = append(ffmpegOpts, extraArgs...)
				//				return errors.New("TODO")
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

	inputFileName := fileName
	if hasMKVChapters {
		logrus.Info("Has MKV chapters, we have to clone the input sadly.")
		tmpMKV := filepath.Join(scratchDir, filepath.Base(stripExtension(fileName))+".nochap.mkv")
		job.TrackFile(tmpMKV, true)
		if err := fileio.Copy(ctx, fileName, tmpMKV); err != nil {
			return errors.Wrap(err, "could not copy file")
		}
		if err := runCommand(ctx, "mkvpropedit", tmpMKV, "--chapters", ""); err != nil {
			return errors.Wrap(err, "could not elide chapters")
		}
		inputFileName = tmpMKV
	}

	baseCmd := append([]string{
		"-nostdin", "-i", inputFileName,
	}, ffmpegOpts...)
	baseCmd = append(baseCmd, "-metadata", "videoproc="+FLAG_VER)

	destFile := stripExtension(fileName) + ".mkv"

	tmpOutFile := filepath.Join(scratchDir, filepath.Base(destFile))
	if decision.Encode.Video.Codec == "" && decision.Encode.Audio.Codec == "" {
		baseCmd = append(baseCmd, "-c", "copy", tmpOutFile)
	} else {
		addArgs := func(args ...string) {
			baseCmd = append(baseCmd, args...)
		}
		addSimpleArg := func(input string, flag string) {
			if input != "" {
				addArgs(flag, input)
			}
		}
		de := decision.Encode
		addArgs("-c:v", de.Video.Codec)
		addSimpleArg(de.Video.Preset, "-preset")
		addSimpleArg(de.Video.CRF, "-crf")
		addSimpleArg(de.Video.Level, "-level")
		addArgs("-c:a", de.Audio.Codec)
		addSimpleArg(de.Audio.Bitrate, "-b:a")
		if de.Deinterlace {
			modArg := false
			for i, arg := range baseCmd {
				if arg == "-vf" {
					baseCmd[i+1] += ",yadif"
					modArg = true
				}
			}
			if !modArg {
				addArgs("-vf", "yadif")
			}
		}
		addArgs(tmpOutFile)
	}
	logrus.Debugf("About to ffmpeg %#v", baseCmd)

	if err := runCommand(ctx, "ffmpeg", baseCmd...); err != nil {
		logrus.Fatal(err)
	}

	if !deleteOriginal && fileName == destFile {
		backupFile := filepath.Join(filepath.Dir(fileName), "backup.orig."+filepath.Base(fileName))
		if err = os.Rename(fileName, backupFile); err != nil {
			return errors.Wrap(err, "could not backup orig")
		}
	}

	if err := fileio.Move(ctx, tmpOutFile, destFile); err != nil {
		return errors.Wrap(err, "could not move")
	}
	if deleteOriginal && fileName != destFile {
		if err := os.Remove(fileName); err != nil {
			return errors.Wrap(err, "could not remove orig")
		}
	}
	return nil
}

func nonCommercialChapters(chapters []Chapter) []Chapter {
	var output []Chapter
	for _, c := range chapters {
		if !c.IsCommercial {
			output = append(output, c)
		}
	}
	return output
}

func copyEncodeRule(dst *videoproc.EncodeConfig, src videoproc.EncodeConfig) {
	takeString(&dst.Video.Codec, src.Video.Codec)
	takeString(&dst.Video.Preset, src.Video.Preset)
	takeString(&dst.Video.CRF, src.Video.CRF)
	takeString(&dst.Video.Level, src.Video.Level)
	takeString(&dst.Audio.Codec, src.Audio.Codec)
	takeString(&dst.Audio.Bitrate, src.Audio.Bitrate)
	if src.Deinterlace {
		dst.Deinterlace = true
	}
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

func ffmpegExtractFilters(ctx context.Context, job *Job, filename string, chapters []Chapter) ([]string, error) {
	var vselect []string
	for _, chapter := range chapters {
		begin := chapter.Begin
		end := chapter.End
		if job.Config.General.RoundCuts {
			begin = math.Floor(begin)
			end = math.Ceil(end)
		}
		vselect = append(vselect, fmt.Sprintf("between(t,%.2f,%.2f)", begin, end))
	}
	betweens := strings.Join(vselect, "+")

	//	extractedFile := filepath.Join(scratchDir, filepath.Base(stripExtension(filename))+".extracted.mkv")
	args := []string{
		//		"-nostdin", "-i", filename,
		"-vf", fmt.Sprintf("select='%s',setpts=N/FRAME_RATE/TB", betweens),
		"-af", fmt.Sprintf("aselect='%s',asetpts=N/SR/TB", betweens),
		//		"-c", "copy",
		//		extractedFile,
		"-map_metadata", "-1",
	}

	//:= "select='between(t,4,6.5)+between(t,17,26)+between(t,74,91)',setpts=N/FRAME_RATE/TB" -af "aselect='between(t,4,6.5)+between(t,17,26)+between(t,74,91)"
	//job.TrackFile(extractedFile, false)
	//	logrus.Infof("About to extract ffmpeg")
	//	err := runCommand(ctx, "ffmpeg", args...)
	return args, nil

}

func extractExistingChapters(ctx context.Context, job *Job, fileName string) ([]Chapter, error) {
	chapterFile := filepath.Join(scratchDir, filepath.Base(stripExtension(fileName))+".extracted.ffmeta")
	err := runCommand(ctx, "ffmpeg", "-i", fileName, "-f", "ffmetadata", chapterFile)
	job.TrackFile(chapterFile, err != nil)
	if err != nil {
		return nil, err
	}
	buf, err := ioutil.ReadFile(chapterFile)
	if err != nil {
		return nil, err
	}
	var chapters []Chapter
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	var timebase int
	for scanner.Scan() {
		line := scanner.Text()
		if line == "[CHAPTER]" {
			chapters = append(chapters, Chapter{})
		} else if len(chapters) != 0 {
			c := &chapters[len(chapters)-1]
			parts := strings.SplitN(line, "=", 2)
			switch strings.ToUpper(parts[0]) {
			case "TIMEBASE":
				timebase, _ = strconv.Atoi(strings.Split(parts[1], "/")[1])
			case "START":
				v, _ := strconv.Atoi(parts[1])
				c.Begin = float64(v) / float64(timebase)
			case "END":
				v, _ := strconv.Atoi(parts[1])
				c.End = float64(v) / float64(timebase)
			case "TITLE":
				c.Name = parts[1]
				c.IsCommercial = strings.HasPrefix(c.Name, "Commercial")
			}
		}
	}
	logrus.Warn(chapters)
	return chapters, nil
}

func getWatchLogIfExists(ctx context.Context, wlDir, fileName string) (*watchlog.WatchLog, error) {
	wlFile, err := watchlog.GenName(wlDir, fileName)
	if err != nil {
		return nil, errors.Wrap(err, "watchlog")
	}

	logrus.Infof("watchlog %s", wlFile)

	if !fileio.IsFile(wlFile) {
		logrus.Infof("watchlog %s does not exist", wlFile)
		return nil, nil
	}

	wl, err := watchlog.Parse(wlFile)
	if err != nil {
		return nil, errors.Wrap(err, "parse watchlog")
	}
	return wl, nil
}

func watchLogChapters(wl *watchlog.WatchLog) []Chapter {
	_, consec := watchlog.DetectSkips(wl.Tape)
	consec = watchlog.FilterConsec(consec)
	var chapters []Chapter
	for i, r := range consec {
		logrus.Infof("range %d from %s to %s", (i + 1), r.Begin.String(), r.End.String())
		chapters = append(chapters, Chapter{
			Begin: float64(r.Begin),
			End:   float64(r.End),
		})
	}
	return chapters
}
