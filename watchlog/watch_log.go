package watchlog

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/crast/videoproc/internal/jsonio"
	"github.com/crast/videoproc/internal/timescale"
)

type WatchLog struct {
	Filename      string           `json:"filename,omitempty"`
	Note          string           `json:"note,omitempty"`
	Special       *WatchLogSpecial `json:"special,omitempty"`
	KnownDuration timescale.Offset `json:"knownDuration,omitempty"`
	KnownSize     int64            `json:"knownSize,omitempty"`
	KnownModTime  string           `json:"knownModTime,omitempty"`

	Consec []Region
	Skips  []Region
	Tape   []OffsetInfo
}

func (wl *WatchLog) EnsureSpecial() *WatchLogSpecial {
	sp := wl.Special
	if sp == nil {
		sp = &WatchLogSpecial{}
		wl.Special = sp
	}
	return sp
}

type WatchLogSpecial struct {
	OverrideStart timescale.Offset `json:"override-start,omitempty"`
	Autoprocess   bool             `json:"autoprocess,omitempty"`
}

type Region struct {
	Begin timescale.Offset
	End   timescale.Offset

	PointCount int `json:"point-count,omitempty"`
}

func (r Region) DisplayString() string {
	return fmt.Sprintf("[%s=>%s]", r.Begin.String(), r.End.String())
}

// FilterConsec filters junk from the consec list
func FilterConsec(regions []Region) []Region {
	hasPointCount := false
	for i := range regions {
		if regions[i].PointCount > 0 {
			hasPointCount = true
			break
		}
	}
	var output []Region
	for _, region := range regions {
		if hasPointCount && region.PointCount < 5 {
			continue
		}

		if (region.End - region.Begin) > 15.0 {
			output = append(output, region)
		}
	}
	return output
}

func Parse(filename string) (*WatchLog, error) {
	var dest WatchLog
	return &dest, jsonio.ReadFile(filename, &dest)
}

func GenName(watchdir string, fileName string) (string, error) {
	fileName, err := filepath.Abs(fileName)
	if err != nil {
		return "", err
	}
	hash := md5.Sum([]byte(filepath.Dir(fileName)))
	prefix := hex.EncodeToString(hash[:10])
	return filepath.Join(watchdir, fmt.Sprintf("%s_%s.json", prefix, filepath.Base(fileName))), nil
}

type OffsetInfo struct {
	timescale.Offset
	Info string
}

func (o OffsetInfo) MarshalJSON() ([]byte, error) {
	if o.Info == "" {
		return o.Offset.MarshalJSON()
	}
	return json.Marshal(timescale.TimestampMKV(o.Offset) + "//" + o.Info)
}

func (o *OffsetInfo) UnmarshalJSON(data []byte) error {
	if data[0] == '"' {
		var dest string
		err := json.Unmarshal(data, &dest)
		if err != nil {
			return err
		}
		parts := strings.SplitN(dest, "//", 2)
		if len(parts) == 2 {
			o.Info = parts[1]
		}
		o.Offset, err = timescale.ParseMKV(parts[0])
		return err
	}
	return o.Offset.UnmarshalJSON(data)
}

func BasicTape(input []OffsetInfo) []timescale.Offset {
	output := make([]timescale.Offset, len(input))
	for i := 0; i < len(input); i++ {
		output[i] = input[i].Offset
	}
	return output
}
