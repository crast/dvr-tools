package logic

import (
	"context"
	"encoding/xml"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/crast/videoproc/internal/jsonio"
	"github.com/crast/videoproc/internal/timescale"
	"github.com/crast/videoproc/watchlog"
)

var Token string
var PlexServer string
var WatchLogDir string

func GlobalPoller(ctx context.Context) {
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
var currentSerial = 0

type State struct {
	Key    string
	Serial int
	File   string
	Chan   chan PlayUpdate
	cancel func()

	mu      sync.Mutex
	cs      currentState
	nilStop bool
}

type currentState struct {
	Prefer        bool
	StartOverride timescale.Offset
	CurrentPos    timescale.Offset
}

func (s *State) WithLock(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *State) Snapshot() currentState {
	var cs currentState
	s.WithLock(func() {
		cs = s.cs
	})
	return cs
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
	s.File = fileName
	logrus.Infof("############### PLAYTAPE BEGIN %s", fileName)

	jsonFileName := fileName + ".watchlog.json"
	if WatchLogDir != "" {
		jsonFileName, err = watchlog.GenName(WatchLogDir, fileName)
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
		if wl.Note == "prefer" {
			s.WithLock(func() {
				s.cs.Prefer = true
			})
		} else if wl.Note == "partial" {
			wl.Note = ""
		}
	}

	wl.Filename = fileName
	durationSec := timescale.FromMillis(video.Duration)
	wl.KnownDuration = durationSec

	positions, err := s.watcherMain(ctx)
	if err != nil {
		logrus.Warnf("Unexpected error in playtape %s", err.Error())
	}
	logrus.Infof("############### PLAYTAPE STOPPED %s", fileName)

	cs := s.Snapshot()
	special := wl.EnsureSpecial()

	if cs.StartOverride > 0.0 {
		special.OverrideStart = cs.StartOverride
		for i, oi := range wl.Tape {
			if oi.Offset < cs.StartOverride {
				logrus.Warnf("Overriding start in restored tape position %d %s => %s", i, oi.Offset, cs.StartOverride)
				wl.Tape[i].Offset = cs.StartOverride
			}
		}
	}

	for i, p := range positions {
		fp := timescale.FromMillis(p.Position)
		if fp < cs.StartOverride {
			logrus.Warnf("Overriding start %s => %s", fp, cs.StartOverride)
			fp = cs.StartOverride
		}
		wl.Tape = append(wl.Tape, watchlog.OffsetInfo{Offset: fp, Info: p.Info})
		logrus.Debugf(" -> %02d: %.1f", i, fp)
	}

	if len(wl.Tape) < 5 {
		return
	}
	fromFullDuration := (durationSec - wl.Tape[len(wl.Tape)-1].Offset)
	if fromFullDuration > 150.0 {
		logrus.Warnf("Didn't watch full duration, marking")
		wl.Note = "partial"
		// logrus.Debugf("Discarded playtape: %#v", playtape)
		// return
	} else if fromFullDuration < 10.0 {
		s.WithLock(func() {
			if s.nilStop {
				logrus.Warnf("Adding nilstop to %s", durationSec)
				wl.Tape = append(wl.Tape, watchlog.OffsetInfo{Offset: durationSec, Info: "nilstop"})
			}
		})
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

	if cs.Prefer {
		wl.Note = "prefer"
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
			if update.ViewOffset == nil {
				s.WithLock(func() { s.nilStop = true })
			}
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
			logrus.Infof("%s %s", timescale.TimestampMKV(timescale.FromMillis(offset)), update.Source)
		}

		if !samePos {
			s.WithLock(func() {
				s.cs.CurrentPos = timescale.FromMillis(offset)
			})
			positions = append(positions, position{offset, update.Time, update.Source})
		}
	}
	logrus.Warn("end of seeker loop")
	return positions, nil
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
		currentSerial += 1
		ctx, cancel := context.WithCancel(context.Background())
		state = &State{
			Key:    key,
			Serial: currentSerial,
			Chan:   make(chan PlayUpdate),
			cancel: cancel,
		}
		go state.Watcher(ctx)
		seen[key] = state
	}
	return state
}
