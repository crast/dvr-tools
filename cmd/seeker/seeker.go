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
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

var token string
var plexServer string

func main() {
	debugMode := flag.Bool("debug", false, "Debug")
	flag.StringVar(&token, "token", "", "Token")
	flag.StringVar(&plexServer, "plex", "http://192.168.1.2:32400", "plex server")
	flag.Parse()
	if *debugMode {
		logrus.SetLevel(logrus.DebugLevel)
	}
	http.HandleFunc("/hook", handleHook)
	go globalPoller(context.Background())
	log.Fatal(http.ListenAndServe(":8081", nil))
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
			fmt.Printf("Part JSON %s\n\n", slurp)
			var dest Event
			err := json.Unmarshal(slurp, &dest)
			if err != nil {
				logrus.Warn(err)
				return
			}
			logrus.Printf("%#v", dest)
			fire(&dest)
		}

	}
	w.Write([]byte("OK"))
}

func globalPoller(ctx context.Context) {
	const smallTimeout = 2000 * time.Millisecond
	const addTimeout = 50 * time.Millisecond
	currentTimeout := smallTimeout
	for {
		var when time.Time
		select {
		case <-ctx.Done():
			return
		case when = <-time.After(currentTimeout):
		}

		buf, err := httpGet(ctx, "/status/sessions")
		if err != nil {
			logrus.Warnf("Error in getting sessions: %s")
			return
		}

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
					Time:     when,
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

func (s *State) Poller(ctx context.Context) {
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

	var positions = []position{
		{Position: dest.Video[0].ViewOffset, Time: time.Now()},
	}
	for update := range s.Chan {
		if update.position.Position == positions[len(positions)-1].Position {
			logrus.Debug("Dropped update", update)
			continue
		}
		positions = append(positions, update.position)
		idx := len(positions) - 1
		if (positions[idx].Position - positions[idx-1].Position) > 11000 {
			logrus.Info("Seek detected %d", idx)
			first := idx - 50
			if first < 0 {
				first = 0
			}
			for i := first; i < len(positions); i++ {
				logrus.Infof(" -> %02d: %d", i, positions[i].Position)
			}
		}
	}

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
				Time:     time.Now(),
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
		go state.Poller(ctx)
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
