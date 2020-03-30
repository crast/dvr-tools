package mediainfo

import (
	"encoding/json"
	"strconv"
)

type QuotedInt int

func (qi *QuotedInt) UnmarshalJSON(buf []byte) error {
	var tmp string
	if err := json.Unmarshal(buf, &tmp); err != nil {
		return err
	}
	n, err := strconv.Atoi(tmp)
	*qi = QuotedInt(n)
	return err
}

func (qi QuotedInt) Int() int {
	return int(qi)
}

type QuotedFloat float64

func (qf *QuotedFloat) UnmarshalJSON(buf []byte) error {
	var tmp string
	if err := json.Unmarshal(buf, &tmp); err != nil {
		return err
	}
	f, err := strconv.ParseFloat(tmp, 64)
	*qf = QuotedFloat(f)
	return err
}

func (qf QuotedFloat) Float() float64 {
	return float64(qf)
}
