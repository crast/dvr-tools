package watchlog

import (
	"github.com/crast/dvr-tools/internal/timescale"
	"github.com/sirupsen/logrus"
)

func DetectSkips(playtape []timescale.Offset) (skips, consec []Region) {
	logrus.Debugf("Detect skips %#v", playtape)
	var currentConsec *Region
	var currentSkip *Region

	for i := 1; i < len(playtape); i++ {
		current := playtape[i]
		if difference := current - playtape[i-1]; difference > 12.0 {
			currentConsec = nil
			if currentSkip == nil || currentSkip.End < playtape[i-1] {
				skips = append(skips, Region{
					Begin: playtape[i-1],
					End:   current,
				})
				currentSkip = &skips[len(skips)-1]
			} else {
				currentSkip.End = playtape[i]
			}
		} else if difference > 0 {
			if currentConsec == nil {
				consec = append(consec, Region{Begin: playtape[i-1], End: current, PointCount: 1})
				currentConsec = &consec[len(consec)-1]
			} else {
				currentConsec.PointCount += 1
				currentConsec.End = current
			}
		} else if difference < -4.0 {
			if currentSkip != nil && currentSkip.End > current {
				currentSkip.End = current
			}
			if currentConsec != nil && currentConsec.Begin > current {
				valid := true
				// ignore skips into other regions
				for _, region := range consec[:len(consec)-1] {
					if current < region.End {
						logrus.Debugf("DetectSkips: Skipped backwards to %s falls into a previous region %s", current.String(), region.DisplayString())
						valid = false
						break
					}
				}
				if valid {
					currentConsec.Begin = current
					currentConsec.End = current
				}
			}
		}
	}
	return skips, consec
}
