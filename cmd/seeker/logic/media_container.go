package logic

import "encoding/xml"

type MediaContainer struct {
	XMLName xml.Name `xml:"MediaContainer"`

	Video []Video `xml:"Video"`
}

type Video struct {
	Key        string `xml:"key,attr"`
	ViewOffset *int64 `xml:"viewOffset,attr"`
	ParentKey  string `xml:"parentKey,attr"`
	Type       string `xml:"type,attr"`
	Duration   int64  `xml:"duration,attr"`

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
