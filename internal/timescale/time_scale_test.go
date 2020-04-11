package timescale

import (
	"math"
	"strconv"
	"testing"
)

func TestMKV(t *testing.T) {
	tests := []struct {
		input  Offset
		expect string
	}{
		{5.77, "00:00:05.770"},
		{65.433, "00:01:05.433"},
		{3990.5, "01:06:30.500"},
	}

	for i, s := range tests {
		t.Run("Case"+strconv.Itoa(i), func(t *testing.T) {
			output := TimestampMKV(s.input)
			if output != s.expect {
				t.Errorf("Timestamp(%f): Expected %s, got %s", float64(s.input), s.expect, output)
			} else {
				revert, err := ParseMKV(output)
				if err != nil {
					t.Errorf("Unexpected error")
				}
				if math.Abs(float64(revert-s.input)) > 0.0009 {
					t.Errorf("%s REVERT: Expected %f, got %f", output, float64(s.input), revert)
				}
			}
		})
	}
}
