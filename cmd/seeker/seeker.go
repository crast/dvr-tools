package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/crast/videoproc/internal/jsonio"
	"github.com/crast/videoproc/internal/timescale"
	"github.com/crast/videoproc/watchlog"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	app "gopkg.in/crast/app.v0"
)

var token string
var plexServer string
var watchLogDir string

func main() {
	debugMode := flag.Bool("debug", false, "Debug")
	flag.StringVar(&token, "token", "", "Token")
	flag.StringVar(&plexServer, "plex", "http://192.168.1.2:32400", "plex server")
	flag.StringVar(&watchLogDir, "log-dir", "", "watch log dir")
	flag.Parse()
	if *debugMode {
		logrus.SetLevel(logrus.DebugLevel)
	}
	http.HandleFunc("/hook", handleHook)

	baseCtx, cancel := context.WithCancel(context.Background())
	app.AddCloser(func() error {
		logrus.Debugf("Running closer")
		cancel()
		return nil
	})

	app.Go(func() {
		globalPoller(baseCtx)
	})

	server := &http.Server{}

	app.AddCloser(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	})

	app.Go(func() error {
		lis, err := net.Listen("tcp", ":8081")
		if err != nil {
			logrus.Fatal(err)
		}
		return server.Serve(lis)

	})

	app.Main()
}

func handleHook(w http.ResponseWriter, req *http.Request) {
	var buf bytes.Buffer
	io.Copy(&buf, req.Body)
	req.Body.Close()
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		logrus.Warn(err.Error())
		return
	}
	mr := multipart.NewReader(&buf, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			logrus.Warn(err)
			return
		}
		if p.Header.Get("Content-Type") == "application/json" {
			slurp, _ := ioutil.ReadAll(p)
			var dest Event
			err := json.Unmarshal(slurp, &dest)
			if err != nil {
				logrus.Warn(err)
				return
			}
			logrus.Debugf("%#v", dest)
			go fire(&dest)
		}

	}
	w.Write([]byte("OK"))
}

func globalPoller(ctx context.Context) {
	const smallTimeout = 2500 * time.Millisecond
	const addTimeout = 50 * time.Millisecond
	currentTimeout := smallTimeout
	errCount := 0
	for {
		var when time.Time
		select {
		case <-ctx.Done():
			return
		case when = <-time.After(currentTimeout):
		}

		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		buf, err := httpGet(reqCtx, "/status/sessions")
		cancel()
		if err != nil {
			errCount += 1
			logrus.Warnf("Error in getting sessions: %s", err.Error())
			currentTimeout += addTimeout

			if errCount > 100 {
				logrus.Fatal("100 failures getting sessions, quitting.")
			}
			continue
		}

		errCount = 0
		var mc MediaContainer
		err = xml.Unmarshal(buf, &mc)
		if err != nil {
			logrus.Warnf("xml error: %s", err.Error())
			continue
		}

		for _, video := range mc.Video {
			mu.Lock()
			state := ensureState(video.Key)
			mu.Unlock()
			if video.ViewOffset == nil {
				logrus.Warn("nil viewOffset", video)
			}
			state.Chan <- PlayUpdate{
				ViewOffset: video.ViewOffset,
				Time:       when.UTC(),
				Source:     "poll",
			}
		}
		if len(mc.Video) != 0 {
			currentTimeout = smallTimeout
		} else if currentTimeout < (30 * time.Second) {
			currentTimeout += addTimeout
		}

	}

}

type Event struct {
	Event    string
	Metadata EventMeta
}

type EventMeta struct {
	LibrarySectionType string
	Key                string `json:"key"`
	ViewOffset         *int64
}

var mu sync.Mutex
var seen = map[string]*State{}

type State struct {
	Key          string
	File         string
	LastPausePos int64
	LastWatchPos int64
	Chan         chan PlayUpdate
	cancel       func()
}

func (s *State) Watcher(ctx context.Context) {
	defer func() {
		mu.Lock()
		delete(seen, s.Key)
		mu.Unlock()
	}()

	buf, err := httpGet(ctx, s.Key)
	if err != nil {
		logrus.Warnf("get error: %s", err.Error())
		return
	}
	var dest MediaContainer
	err = xml.Unmarshal(buf, &dest)
	if err != nil {
		logrus.Warnf("xml error: %s", err.Error())
		return

	}
	logrus.Debugf("XML MediaContainer %#v", dest)

	video := dest.Video[0]

	fileName := video.Media[0].Part[0].File
	logrus.Infof("############### PLAYTAPE BEGIN %s", fileName)

	jsonFileName := fileName + ".watchlog.json"
	if watchLogDir != "" {
		jsonFileName, err = watchlog.GenName(watchLogDir, fileName)
		if err != nil {
			logrus.Warnf("Failure making watchlog name: %s", err.Error())
			return
		}
	}

	wl, err := watchlog.Parse(jsonFileName)
	if err != nil {
		if !os.IsNotExist(errors.Cause(err)) {
			logrus.Warnf("Failure parsing old watchlog: %s", err.Error())
			return
		}

		wl = &watchlog.WatchLog{
			Note: "unknown",
		}
	} else {
		logrus.Infof("restored playtape with %d points", len(wl.Tape))
	}

	wl.Filename = fileName

	positions, err := s.watcherMain(ctx)
	if err != nil {
		logrus.Warnf("Unexpected error in playtape %s", err.Error())
	}
	logrus.Infof("############### PLAYTAPE STOPPED %s", fileName)

	for i, p := range positions {
		fp := timescale.FromMillis(p.Position)
		wl.Tape = append(wl.Tape, watchlog.OffsetInfo{Offset: fp, Info: p.Info})
		logrus.Debugf(" -> %02d: %.1f", i, fp)
	}

	durationSec := timescale.FromMillis(video.Duration)

	if len(wl.Tape) < 5 {
		return
	} else if (durationSec - wl.Tape[len(wl.Tape)-1].Offset) > 150.0 {
		logrus.Warnf("Didn't watch full duration, marking")
		wl.Note = "partial"
		// logrus.Debugf("Discarded playtape: %#v", playtape)
		// return
	}

	wl.Skips, wl.Consec = watchlog.DetectSkips(watchlog.BasicTape(wl.Tape))

	if len(wl.Skips) == 0 {
		wl.Note = "noskip"
	}

	for i, skip := range wl.Skips {
		logrus.Infof("Skip %d: %.1f => %.1f ( %s => %s )", i, skip.Begin, skip.End, timescale.TimestampMKV(skip.Begin), timescale.TimestampMKV(skip.End))
	}

	for i, r := range wl.Consec {
		logrus.Infof("Consecutive %d: %.1f => %.1f ( %s => %s )", i, r.Begin, r.End, timescale.TimestampMKV(r.Begin), timescale.TimestampMKV(r.End))
	}

	err = jsonio.WriteFile(jsonFileName, wl)
	if err != nil {
		logrus.Warnf("Could not make watchlog %s: %s", jsonFileName, err.Error())
	}
}

func (s *State) watcherMain(ctx context.Context) ([]position, error) {
	var once sync.Once
	shutdown := func() {
		close(s.Chan)
	}
	defer once.Do(shutdown)

	var positions = []position{
		//(<-s.Chan).position,
	}

	stopTicker := time.NewTicker(10 * time.Second)
	stopCounts := 0

	for {
		var update PlayUpdate
		var ok bool
		select {
		case <-ctx.Done():
			return positions, ctx.Err()
		case <-stopTicker.C:
			stopCounts += 1
			if stopCounts >= 7 {
				once.Do(shutdown)
			}
			continue
		case update, ok = <-s.Chan:
			if !ok {
				return positions, nil
			}
		}

		if update.Source == "media.stop" {
			logrus.Debug("stop timer set")
			stopCounts += 3
		} else if stopCounts > 0 && update.ViewOffset != nil {
			stopCounts = 0
		}

		if len(positions) > 0 && update.ViewOffset != nil && *update.ViewOffset == 0 && positions[len(positions)-1].Position > 13000 {
			if update.Source != "media.stop" && update.Source != "media.play" {
				logrus.Warnf("Unexplained zero event %v", update)
			}
			continue
		}

		if update.ViewOffset == nil {
			logrus.Info(update.Source)
			continue
		}
		offset := *update.ViewOffset

		samePos := (len(positions) > 0 && offset == positions[len(positions)-1].Position)

		if !samePos || strings.HasPrefix(update.Source, "media.") {
			logrus.Infof("%s at %s", update.Source, timestampMKV(float64(offset)/1000.0))
		}

		if !samePos {
			positions = append(positions, position{offset, update.Time, update.Source})
		}
	}
	logrus.Warn("end of seeker loop")
	return positions, nil
}

func timestampMKV(floatSeconds float64) string {
	rawSeconds := int(floatSeconds)
	seconds := rawSeconds % 60
	rawMinutes := rawSeconds / 60
	hours := rawMinutes / 60

	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, rawMinutes%60, seconds, int((floatSeconds-float64(rawSeconds))*1000.0))
}

type position struct {
	Position int64
	Time     time.Time
	Info     string
}

type PlayUpdate struct {
	ViewOffset *int64
	Time       time.Time
	Source     string
}

func fire(event *Event) {
	mu.Lock()
	defer mu.Unlock()

	switch event.Event {

	case "media.pause", "media.resume", "media.stop", "media.play":
		logrus.Debugf("event %s %#v", event.Event, event)
		if event.Metadata.ViewOffset == nil {
			logrus.Warnf("nil viewoffset %s", event.Event)
		}

		state := ensureState(event.Metadata.Key)
		state.Chan <- PlayUpdate{
			ViewOffset: event.Metadata.ViewOffset,
			Time:       time.Now().UTC(),
			Source:     event.Event,
		}

	default:
		logrus.Warnf("Unknown event %s %#v", event.Event, event)
	}
}

func ensureState(key string) *State {
	state := seen[key]
	if state == nil {
		ctx, cancel := context.WithCancel(context.Background())
		state = &State{
			Key:    key,
			Chan:   make(chan PlayUpdate),
			cancel: cancel,
		}
		go state.Watcher(ctx)
		seen[key] = state
	}
	return state
}

type MediaContainer struct {
	XMLName xml.Name `xml:"MediaContainer"`

	Video []Video `xml:"Video"`
}

type Video struct {
	Key        string `xml:"key,attr"`
	ViewOffset *int64 `xml:"viewOffset,attr"`
	ParentKey  string `xml:"parentKey,attr"`
	Type       string `xml:"type,attr"`
	Duration   int64  `xml:"duration,attr"`

	Media []Media
	Genre []Genre
}
type Genre struct {
	ID  string `xml:"id,attr"`
	Tag string
}

type Media struct {
	ID       string `xml:"id,attr"`
	Duration int64  `xml:"duration,attr"`

	Part []Part
}

type Part struct {
	ID   string `xml:"id,attr"`
	File string `xml:"file,attr"`
}

func httpGet(ctx context.Context, path string) ([]byte, error) {
	logrus.Debug("about to get ", plexServer+path)
	req, err := http.NewRequestWithContext(ctx, "GET", plexServer+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("X-Plex-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}
