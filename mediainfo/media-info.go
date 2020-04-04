package mediainfo

import (
	"encoding/json"
)

type MediaInfo struct {
	Media Media
}

type Media struct {
	Ref    string     `json:"@ref"`
	Tracks []AnyTrack `json:"track"`
}

type AnyTrack struct {
	Track interface{}
}

func (at *AnyTrack) UnmarshalJSON(buf []byte) error {
	var gt genericTrack
	json.Unmarshal(buf, &gt)
	var dest interface{}
	switch gt.Type {
	case "General":
		dest = &GeneralTrack{}
	case "Video":
		dest = &VideoTrack{}
	case "Audio":
		dest = &AudioTrack{}
	default:
		dest = &MediaTrackMixin{}
	}
	err := json.Unmarshal(buf, dest)
	at.Track = dest
	return err
}

type genericTrack struct {
	Type string `json:"@type"`
}

type GeneralTrack struct {
	FileExtension string      `json:"FileExtension"`
	Duration      QuotedFloat `json:"Duration"`

	MediaTrackMixin
}

type MediaTrackMixin struct {
	SegmentType   string            `json:"@type"`
	ID            string            `json:"ID"`
	UniqueID      string            `json:"UniqueID"`
	Format        string            `json:"Format"`
	FormatVersion string            `json:"Format_Version"`
	Extra         map[string]string `json:"extra"`
}

type VideoTrack struct {
	MediaTrackMixin
	FormatProfile string `json:"Format_Profile"`

	Width            QuotedInt `json:"Width"`
	Height           QuotedInt `json:"Height"`
	PixelAspectRatio string    `json:"PixelAspectRatio"`
	ScanType         string    `json:"ScanType"`
}

type AudioTrack struct {
	MediaTrackMixin
}
