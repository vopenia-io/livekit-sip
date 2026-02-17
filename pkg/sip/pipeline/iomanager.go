package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

func NewIOChain(log logger.Logger, parent *Pipeline) *IOManager {
	return &IOManager{
		log:      log,
		pipeline: parent,
	}
}

type IOManager struct {
	pipeline *Pipeline
	log      logger.Logger

	SipController *gst.Element
	LkController  *gst.Element
}

var _ GstChain = (*IOManager)(nil)

// Create implements [GstChain].
func (c *IOManager) Create() error {
	var err error

	c.SipController, err = gst.NewElement("io_manager_sip")
	if err != nil {
		return fmt.Errorf("failed to create IO Manager SIP element: %w", err)
	}

	c.LkController, err = gst.NewElement("io_manager_livekit")
	if err != nil {
		return fmt.Errorf("failed to create IO Manager LiveKit element: %w", err)
	}

	return nil
}

func (c *IOManager) Add() error {
	if err := c.pipeline.Pipeline().AddMany(
		c.SipController,
		c.LkController,
	); err != nil {
		return fmt.Errorf("failed to add IO Manager elements to pipeline: %w", err)
	}
	return nil
}

func (c *IOManager) handleSipControllerPadAdded(_ *gst.Element, pad *gst.Pad) {
	pname := pad.GetName()
	c.log.Debugw("SIP IO Manager pad added", "pad", pname)

	if !strings.HasPrefix(pname, "send_rtp_src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session); err != nil {
		c.log.Errorw("Failed to parse pad name", err, "pad", pname)
		return
	}

	destPad := c.pipeline.WebrtcIo.WebrtcRtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if destPad == nil {
		c.log.Errorw("Failed to get request pad", nil, "pad", fmt.Sprintf("send_rtp_sink_%d", session))
		return
	}

	if ret := pad.Link(destPad); ret != gst.PadLinkOK {
		c.log.Errorw("Failed to link pads", nil, "result", ret, "src_pad", pad.GetName(), "dest_pad", destPad.GetName())
		return
	}
}

func (c *IOManager) handleLkControllerPadAdded(_ *gst.Element, pad *gst.Pad) {
	pname := pad.GetName()
	c.log.Debugw("Livekit IO Manager pad added", "pad", pname)

	if !strings.HasPrefix(pname, "send_rtp_src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session); err != nil {
		c.log.Errorw("Failed to parse pad name", err, "pad", pname)
		return
	}

	destPad := c.pipeline.SipIo.SipRtpBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if destPad == nil {
		c.log.Errorw("Failed to get request pad", nil, "pad", fmt.Sprintf("send_rtp_sink_%d", session))
		return
	}

	if ret := pad.Link(destPad); ret != gst.PadLinkOK {
		c.log.Errorw("Failed to link pads", nil, "result", ret, "src_pad", pad.GetName(), "dest_pad", destPad.GetName())
		return
	}
}

func (c *IOManager) Link() error {
	cweak := weak.Make(c)

	c.SipController.Connect("pad-added", func(e *gst.Element, pad *gst.Pad) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.handleSipControllerPadAdded(e, pad)
	})

	c.LkController.Connect("pad-added", func(e *gst.Element, pad *gst.Pad) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.handleLkControllerPadAdded(e, pad)
	})

	return nil
}

func (c *IOManager) Close() error {
	if err := c.pipeline.Pipeline().RemoveMany(
		c.SipController,
		c.LkController,
	); err != nil {
		return fmt.Errorf("failed to remove IO Manager elements from pipeline: %w", err)
	}
	return nil
}
