package logic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/crast/dvr-tools/internal/timescale"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

func GetRouter() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/", homeHandler)
	r.HandleFunc("/hook", handleHook)
	r.HandleFunc("/item/{serial:[0-9]+}/override-start", overrideStart)
	r.HandleFunc("/item/{serial:[0-9]+}/prefer/{value:[0-9]}", preferHandler)
	r.HandleFunc("/item/{serial:[0-9]+}/autoproc/{value:[0-9]}", entryRoute(autoprocHandler))
	return r
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

func homeHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "max-age=20")
	entries := entriesBySerial(true)
	if err := homeTemplate.Execute(w, entries); err != nil {
		logrus.Error(err)
	}
}

func overrideStart(w http.ResponseWriter, req *http.Request) {
	serial := getSerial(req)
	if serial < 0 {
		http.Error(w, "bad serial", 400)
		return
	}
	entries := entriesBySerial(false)
	if e := entries[serial]; e != nil {
		var ts timescale.Offset
		e.State.WithLock(func() {
			ts = e.State.cs.CurrentPos
			e.State.cs.StartOverride = ts
		})
		renderRedirect(w, fmt.Sprintf("Start overridden to %s", ts.String()))
	} else {
		renderRedirect(w, fmt.Sprintf("can't find entry with serial %s", serial))
	}
}

func preferHandler(w http.ResponseWriter, req *http.Request) {
	serial := getSerial(req)
	if serial < 0 {
		http.Error(w, "bad serial", 400)
		return
	}
	val := (mux.Vars(req)["value"] == "1")
	entries := entriesBySerial(false)
	if e := entries[serial]; e != nil {
		e.State.WithLock(func() {
			e.State.cs.Prefer = val
		})
		renderRedirect(w, fmt.Sprintf("Preferring %s == %v", e.State.File, val))
		return
	}
}

func autoprocHandler(w http.ResponseWriter, req *http.Request, entry *homeEntry) {
	val := (mux.Vars(req)["value"] == "1")
	entry.State.WithLock(func() {
		entry.State.cs.Autoprocess = val
	})
	renderRedirect(w, fmt.Sprintf("Autoprocess %s == %v", entry.State.File, val))
}

func serialRoute(f func(http.ResponseWriter, *http.Request, int)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		serial := getSerial(req)
		if serial < 0 {
			http.Error(w, "bad serial", 400)
			return
		}
		f(w, req, serial)
	}
}

func entryRoute(f func(http.ResponseWriter, *http.Request, *homeEntry)) http.HandlerFunc {
	return serialRoute(func(w http.ResponseWriter, req *http.Request, serial int) {
		entries := entriesBySerial(false)
		if entry := entries[serial]; entry != nil {
			f(w, req, entry)
		} else {
			http.Error(w, "Entry not found", 404)
		}
	})
}

func getSerial(req *http.Request) int {
	rawSerial := mux.Vars(req)["serial"]
	serial, err := strconv.Atoi(rawSerial)
	if err != nil {
		return -1
	}
	return serial
}

type homeEntry struct {
	*State
	currentState
	ShortFile string
}

func entriesBySerial(annotate bool) map[int]*homeEntry {
	mu.Lock()
	entries := make(map[int]*homeEntry, len(seen))
	for _, s := range seen {
		entries[s.Serial] = &homeEntry{State: s, ShortFile: filepath.Base(s.File)}
	}
	mu.Unlock()
	if annotate {
		for _, e := range entries {
			e.currentState = e.State.Snapshot()
		}
	}
	return entries
}

func render(w http.ResponseWriter, t *template.Template, input interface{}) error {
	var buf bytes.Buffer
	if err := t.Execute(&buf, input); err != nil {
		logrus.Error(err)
		http.Error(w, err.Error(), 500)
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := io.Copy(w, &buf)
	return err
}

var homeTemplate = template.Must(template.New("home").Parse(`
<html>
<head>
	<meta http-equiv="refresh" content="30;URL='/?a'" />   
</head>
<body>
	<h2>Currently Watching</h2>
	<table>
	<tr>
		<th>File</th>
		<th>Position</th>
		<th>Override</th>
		<th>Prefer</th>
		<th>Autoprocess</th>
	</tr>
	{{range $serial, $s := . }}
		<tr>
			<td><a href="/item/{{ $serial }}">{{ $s.ShortFile }}</a></td>
			<td>{{ $s.CurrentPos }}</td>
			<td><a href="/item/{{ $serial }}/override-start?p={{ $s.CurrentPos }}" {{ if $s.StartOverride }}onclick="return confirm('already overridden. really override?')"{{ end }}>{{ if $s.StartOverride }}already overridden {{ $s.StartOverride }}{{ else }}override start{{ end }}</a></td>
			<td><a href="/item/{{ $serial }}/prefer/{{ if $s.Prefer }}0{{ else }}1{{ end }}">{{ if $s.Prefer }}UN-prefer{{ else }}prefer{{ end }}</a></td>
			<td><a href="/item/{{ $serial }}/autoproc/{{ if $s.Autoprocess }}0{{ else }}1{{ end }}">{{ if $s.Autoprocess }}<b>(enabled)</b>{{ else }}autoprocess{{ end }}</a></td>
		</tr>
	{{ end }}
	</table>
</body>
</html>
`))

func renderRedirect(w http.ResponseWriter, message string) {
	render(w, redirectTemplate, redirectInput{
		Timestamp: time.Now().UnixNano(),
		Title:     "",
		Message:   message,
	})
}

type redirectInput struct {
	Timestamp int64
	Title     string
	Message   string
}

var redirectTemplate = template.Must(template.New("redirect").Parse(`
<html>
<head>
	<meta http-equiv="refresh" content="5;URL='/?{{ .Timestamp }}'" />   
</head>
<body>
{{ .Message }}
</body>
</html>
`))
