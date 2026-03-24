package samplewriter

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"samplewriter",
		gst.RankNone,
		&SampleWriter{},
		base.ExtendsBaseSrc,
	)
}
