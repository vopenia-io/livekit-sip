package activeselector

import (
	"fmt"
	"reflect"
	"runtime"

	"github.com/go-gst/go-gst/gst"
)

var ACTIVE_TRACK_EVENT_NAME = reflect.TypeFor[ActiveTrackEvent]().Name()

type ActiveTrackEvent struct {
	TrackSSRC uint32
	TrackKind uint32
}

func (e *ActiveTrackEvent) Marshal() (*gst.Structure, error) {
	structure := gst.NewStructure(ACTIVE_TRACK_EVENT_NAME)

	if err := structure.SetValue("track-ssrc", e.TrackSSRC); err != nil {
		return nil, err
	}
	if err := structure.SetValue("track-kind", e.TrackKind); err != nil {
		return nil, err
	}
	return structure, nil
}

func (e *ActiveTrackEvent) MarshalEvent() (*gst.Event, error) {
	structure, err := e.Marshal()
	if err != nil {
		return nil, err
	}

	runtime.SetFinalizer(structure, nil)

	event := gst.NewCustomEvent(gst.EventTypeCustomDownstream, structure)
	return event, nil
}

func (e *ActiveTrackEvent) Unmarshal(structure *gst.Structure) error {
	if structure == nil {
		return fmt.Errorf("structure is nil")
	}

	if structure.Name() != ACTIVE_TRACK_EVENT_NAME {
		return fmt.Errorf("invalid structure name: %s", structure.Name())
	}

	ssrc, err := structure.GetValue("track-ssrc")
	if err != nil {
		return err
	}
	trackSSRC, ok := ssrc.(uint)
	if !ok {
		return fmt.Errorf("invalid type for track-ssrc: %T", ssrc)
	}

	kind, err := structure.GetValue("track-kind")
	if err != nil {
		return err
	}
	trackKind, ok := kind.(uint)
	if !ok {
		return fmt.Errorf("invalid type for track-kind: %T", kind)
	}

	e.TrackSSRC = uint32(trackSSRC)
	e.TrackKind = uint32(trackKind)
	return nil
}
