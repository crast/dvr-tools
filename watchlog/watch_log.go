package watchlog

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/crast/videoproc/internal/jsonio"
	"github.com/crast/videoproc/internal/timescale"
)

type WatchLog struct {
	Filename string `json:"filename,omitempty"`
	Note     string `json:",omitempty"`
	Consec   []Region
	Skips    []Region
	Tape     []timescale.Offset
}

type Region struct {
	Begin timescale.Offset
	End   timescale.Offset
}

// FilterConsec filters junk from the consec list
func FilterConsec(regions []Region) []Region {
	var output []Region
	for _, region := range regions {
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
