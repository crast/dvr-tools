package timescale

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Offset float64

func (o Offset) MarshalJSON() ([]byte, error) {
	return json.Marshal(TimestampMKV(o))
}

func (o *Offset) UnmarshalJSON(data []byte) error {
	if data[0] == '"' {
		var dest string
		err := json.Unmarshal(data, &dest)
		if err != nil {
			return err
		}
		*o, err = ParseMKV(dest)
		return err
	}
	var fdest float64
	err := json.Unmarshal(data, &fdest)
	if err != nil {
		return err
	}
	*o = Offset(fdest)
	return nil
}

func (o Offset) Float() float64 {
	return float64(o)
}

func (o Offset) String() string {
	return TimestampMKV(o)
}

func FromMillis(millis int64) Offset {
	return Offset(millis) / 1000.0
}

func TimestampMKV(floatSeconds Offset) string {
	rawSeconds := int(floatSeconds)
	rawMinutes := rawSeconds / 60
	hours := rawMinutes / 60

	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, rawMinutes%60, rawSeconds%60, int(math.Round(float64(floatSeconds)*1000))%1000)
}

func ParseMKV(s string) (Offset, error) {
	parts := strings.Split(s, ":")
	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	return Offset(seconds + float64(minutes*60) + float64(hours*3600)), nil
}
