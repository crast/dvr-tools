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
	"sync"
	"time"

	"github.com/crast/videoproc/internal/jsonio"
	"github.com/crast/videoproc/watchlog"
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
			state.Chan <- PlayUpdate{
				position: position{
					Position: video.ViewOffset,
					Time:     when.UTC(),
				},
				Source: "poller",
			}
		}
		if len(mc.Video) == 0 {
			currentTimeout += addTimeout
		} else {
			currentTimeout = smallTimeout
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
	ViewOffset         int64
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
	buf, err := httpGet(ctx, s.Key)
	if err != nil {
		logrus.Warnf("get error: %s", err.Error())

		return
	}
	var dest MediaContainer
	err = xml.Unmarshal(buf, &dest)
	if err != nil {
		logrus.Warnf("xml error: %s", err.Error())
	}
	logrus.Warnf("%#v", dest)

	video := dest.Video[0]

	fileName := video.Media[0].Part[0].File
	logrus.Infof("############### PLAYTAPE BEGIN %s", fileName)

	positions, err := s.watcherMain(ctx, video.ViewOffset)
	if err != nil {
		logrus.Warnf("Unexpected error in playtape %s", err.Error())
	}
	logrus.Infof("############### PLAYTAPE STOPPED %s", fileName)

	var playtape []float64
	for i, p := range positions {
		fp := float64(p.Position) / 1000.0
		playtape = append(playtape, fp)
		logrus.Debugf(" -> %02d: %.1f", i, fp)
	}

	durationSec := float64(video.Duration) / 1000.0

	wl := &watchlog.WatchLog{
		Filename: fileName,
		Tape:     playtape,
		Note:     "unknown",
	}

	if len(playtape) < 5 {
		return
	} else if (durationSec - playtape[len(playtape)-1]) > 150.0 {
		logrus.Warnf("Didn't watch full duration, marking")
		wl.Note = "partial"
		// logrus.Debugf("Discarded playtape: %#v", playtape)
		// return
	}

	wl.Skips, wl.Consec = watchlog.DetectSkips(playtape)

	if len(wl.Skips) == 0 {
		wl.Note = "noskip"
	}

	for i, skip := range wl.Skips {
		logrus.Infof("Skip %d: %.1f => %.1f ( %s => %s )", i, skip.Begin, skip.End, timestampMKV(skip.Begin), timestampMKV(skip.End))
	}

	for i, r := range wl.Consec {
		logrus.Infof("Consecutive %d: %.1f => %.1f ( %s => %s )", i, r.Begin, r.End, timestampMKV(r.Begin), timestampMKV(r.End))
	}

	jsonFileName := fileName + ".watchlog.json"
	if watchLogDir != "" {
		jsonFileName, err = watchlog.GenName(watchLogDir, fileName)
		if err != nil {
			logrus.Warnf("Failure making watchlog name: %s", err.Error())
			return
		}
	}

	err = jsonio.WriteFile(jsonFileName, wl)
	if err != nil {
		logrus.Warnf("Could not make watchlog %s: %s", jsonFileName, err.Error())
	}
}

func (s *State) watcherMain(ctx context.Context, initialPos int64) ([]position, error) {
	var once sync.Once
	shutdown := func() {
		mu.Lock()
		delete(seen, s.Key)
		mu.Unlock()
		close(s.Chan)
	}
	defer once.Do(shutdown)

	var positions = []position{
		{Position: initialPos, Time: time.Now().UTC()},
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
		} else if stopCounts > 0 && update.Position != 0 {
			stopCounts = 0
		}

		if update.position.Position == 0 {
			if update.Source != "media.stop" && update.Source != "media.play" {
				logrus.Warnf("Unexplained zero event %v", update)
			}
			continue
		} else if update.position.Position == positions[len(positions)-1].Position {
			logrus.Debugf("Dropped update %v", update)
			continue
		}

		positions = append(positions, update.position)
		logrus.Infof("at %s", timestampMKV(float64(update.position.Position)/1000.0))
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
}

type PlayUpdate struct {
	position
	Source string
}

func fire(event *Event) {
	mu.Lock()
	defer mu.Unlock()

	switch event.Event {

	case "media.pause", "media.resume", "media.stop", "media.play":
		logrus.Debugf("event %s %#v", event.Event, event)

		state := ensureState(event.Metadata.Key)
		state.Chan <- PlayUpdate{
			position: position{
				Position: event.Metadata.ViewOffset,
				Time:     time.Now().UTC(),
			},
			Source: event.Event,
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
	ViewOffset int64  `xml:"viewOffset,attr"`
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
