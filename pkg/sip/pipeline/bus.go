package pipeline

import (
	"errors"
	"fmt"
	"weak"

	"github.com/go-gst/go-gst/gst"
)

func (p *Pipeline) SetupBus() {
	if p.bus != nil {
		p.Log.Errorw("Bus already set up", errors.New("bus already set up"))
		return
	}

	p.bus = p.Pipeline().GetPipelineBus()
	p.Log.Debugw("Setting bus to non-flushing")
	p.bus.SetFlushing(false)

	pweak := weak.Make(p)
	if !p.bus.AddWatch(func(msg *gst.Message) bool {
		p := pweak.Value()
		if p == nil {
			fmt.Printf("Pipeline has been garbage collected, stopping bus watch\n")
			return false
		}
		return p.onMessage(msg)
	}) {
		p.Log.Errorw("Failed to set bus to non-flushing", nil)
	}
}

func (p *Pipeline) CloseBus() {
	if p.bus == nil {
		p.Log.Warnw("Bus not set up, cannot close", nil)
		return
	}
	p.bus.SetFlushing(true)
	p.bus.RemoveWatch()
	p.bus = nil
}

func (p *Pipeline) onMessage(msg *gst.Message) bool {
	p.Log.Debugw("Received bus message", "type", msg.Type())
	switch msg.Type() {
	case gst.MessageError:
		gErr := msg.ParseError()
		p.Log.Errorw("Pipeline error", gErr)
	case gst.MessageStateChanged:
		oldState, newState := msg.ParseStateChanged()
		p.Log.Debugw("Pipeline state changed", "old", oldState.String(), "new", newState.String())
	case gst.MessageElement:
		structure := msg.GetStructure()
		if structure == nil {
			p.Log.Warnw("Received element message with no structure", nil)
			return true
		}
		p.Log.Debugw("Received element message", "structure", structure.String())
		if structure.Name() == "dtmf-event" {
			nbVal, err := structure.GetValue("number")
			if err != nil || nbVal == nil {
				p.Log.Warnw("Received dtmf-event message with no number field", err, "nbVal", nbVal)
				return true
			}
			nb, ok := nbVal.(int)
			if !ok {
				p.Log.Warnw("Received dtmf-event message with invalid number field", nil, "nbVal", fmt.Sprintf("%T=%v", nbVal, nbVal))
				return true
			}
			p.Log.Infow("Received dtmf-event message", "number", nb)
			p.dtmfCh <- nb
		}
	default:
		p.Log.Debugw("Unhandled bus message", "type", msg.Type())
	}
	return true
}

func (p *Pipeline) DTMF() chan int {
	return p.dtmfCh
}
