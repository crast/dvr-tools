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

	"github.com/crast/videoproc/internal/timescale"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

func GetRouter() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/", homeHandler)
	r.HandleFunc("/hook", handleHook)
	r.HandleFunc("/item/{serial:[0-9]+}/override-start", overrideStart)
	r.HandleFunc("/item/{serial:[0-9]+}/prefer/{value:[0-9]}", preferHandler)
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
	entries := entriesBySerial(true)
	if err := homeTemplate.Execute(w, entries); err != nil {
		logrus.Error(err)
	}
}

func overrideStart(w http.ResponseWriter, req *http.Request) {
	serial := getSerial(req)
	if serial < 0 {
		w.Write([]byte("bad serial"))
		return
	}
	entries := entriesBySerial(false)
	if e := entries[serial]; e != nil {
		var ts timescale.Offset
		e.State.WithLock(func() {
			ts = e.State.cs.CurrentPos
			e.State.cs.StartOverride = ts
		})
		fmt.Fprintf(w, "Start overridden to %s", ts.String())
	} else {
		fmt.Fprintf(w, "can't find entry with serial %s", serial)
	}
}

func preferHandler(w http.ResponseWriter, req *http.Request) {
	serial := getSerial(req)
	if serial < 0 {
		w.Write([]byte("bad serial"))
		return
	}
	val := (mux.Vars(req)["value"] == "1")
	entries := entriesBySerial(false)
	if e := entries[serial]; e != nil {
		e.State.WithLock(func() {
			e.State.cs.Prefer = val
		})
		fmt.Fprintf(w, "Preferring %s == %v", e.State.File, val)
	}
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

var homeTemplate = template.Must(template.New("home").Parse(`
<html>
<body>
	<h2>Currently Watching</h2>
	<table>
	<tr>
		<th>File</th>
		<th>Position</th>
		<th>Override</th>
		<th>Prefer</th>
	</tr>
	{{range $serial, $s := . }}
		<tr>
			<td><a href="/item/{{ $serial }}">{{ $s.ShortFile }}</a></td>
			<td>{{ $s.CurrentPos }}</td>
			<td><a href="/item/{{ $serial }}/override-start?p={{ $s.CurrentPos }}">{{ if $s.StartOverride }}already overridden {{ $s.StartOverride }}{{ else }}override start{{ end }}</a></td>
			<td><a href="/item/{{ $serial }}/prefer/{{ if $s.Prefer }}0{{ else }}1{{ end }}">{{ if $s.Prefer }}UN-prefer{{ else }}prefer{{ end }}</a></td>
		</tr>
	{{ end }}
	</table>
</body>
</html>
`))
