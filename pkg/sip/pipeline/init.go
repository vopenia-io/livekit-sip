package pipeline

import (
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/activeselector"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/g711opus"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264vp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/lkroom"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/opusg711"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sinkwriter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipmanager"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sourcereader"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/vp8h264"
)

var mainLoop *glib.MainLoop

func init() {
	gst.Init(nil)
	if !sourcereader.Register() {
		panic("failed to register sourcereader element")
	}

	if !sinkwriter.Register() {
		panic("failed to register sinkwriter element")
	}

	if !h264vp8.Register() {
		panic("Failed to register h264-vp8")
	}

	if !vp8h264.Register() {
		panic("Failed to register vp8-h264")
	}

	if !g711opus.Register() {
		panic("Failed to register g711-opus")
	}

	if !opusg711.Register() {
		panic("Failed to register opus-g711")
	}

	if !sipmanager.Register() {
		panic("Failed to register sipmanager")
	}

	if !lkroom.Register() {
		panic("Failed to register lkroom")
	}

	if !activeselector.Register() {
		panic("Failed to register active-selector")
	}

	mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)
	_ = mainLoop
}
