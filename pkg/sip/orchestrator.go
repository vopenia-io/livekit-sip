package sip

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/vopenia-io/go-pjmedia/pj"
)

var (
	ErrWrongState = errors.New("media orchestrator in wrong state")
)

var doInitCodecs sync.Once

func initCodecs() {
	doInitCodecs.Do(func() {
		pj.RegisterG711Codec(pj.DefaultPjEndpt())
		pj.RegisterH264Codec(pj.DefaultPjEndpt())
	})
}

const (
	ScreenshareMSTreamID = 2
)

type AudioInfo interface {
	Port() uint16
	Codec() *sdpv2.Codec
	AvailableCodecs() []*sdpv2.Codec
	SetMedia(media *sdpv2.SDPMedia)
}

type dispatchOperation struct {
	fn   func() error
	done chan error
}

type MediaState int

const (
	MediaStateFailed MediaState = iota - 1
	MediaStateNew
	MediaStateOK
	MediaStateReady
	MediaStateStarted
	MediaStateStopped
)

func (ms MediaState) String() string {
	switch ms {
	case MediaStateFailed:
		return "failed"
	case MediaStateNew:
		return "new"
	case MediaStateReady:
		return "ready"
	case MediaStateStarted:
		return "started"
	case MediaStateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

type MediaOrchestrator struct {
	ctx     context.Context
	cancel  context.CancelFunc
	log     logger.Logger
	opts    *MediaOptions
	inbound *sipInbound

	dispatchCH chan dispatchOperation
	dispatchOK atomic.Bool
	wg         sync.WaitGroup

	pjpool *pj.PjPool

	pipeline *pipeline.Pipeline
	// bfcp     *BFCPManager

	state MediaState
}

func NewMediaOrchestrator(log logger.Logger, ctx context.Context, inbound *sipInbound, room *Room, opts *MediaOptions) (*MediaOrchestrator, error) {
	initCodecs()
	ctx, cancel := context.WithCancel(ctx)
	o := &MediaOrchestrator{
		ctx:        ctx,
		cancel:     cancel,
		log:        log,
		opts:       opts,
		inbound:    inbound,
		dispatchCH: make(chan dispatchOperation, 1),
		state:      MediaStateNew,
	}

	o.wg.Add(1)
	go o.dispatchLoop()
	for !o.dispatchOK.Load() {
		runtime.Gosched() // wait for dispatch loop to start
	}

	if err := o.dispatch(func() error {
		return o.init(room)
	}); err != nil {
		return nil, err
	}

	return o, nil
}

func (o *MediaOrchestrator) init(room *Room) error {
	if err := o.okStates(MediaStateNew); err != nil {
		return err
	}

	pipeline, err := pipeline.New(o.ctx, o.log, pipeline.SipOpt{
		IP:        o.opts.IP.String(),
		PortStart: uint16(o.opts.Ports.Start),
		PortEnd:   uint16(o.opts.Ports.End),
	})
	if err != nil {
		return fmt.Errorf("could not create pipeline: %w", err)
	}
	o.pipeline = pipeline

	pipeline.Monitor()

	// o.bfcp = NewBFCPManager(o.ctx, o.log, o.opts, o.inbound)

	if err := o.pipeline.SetStateWait(gst.StateReady); err != nil {
		return fmt.Errorf("failed to set pipeline to ready state: %w", err)
	}

	o.state = MediaStateOK

	return nil
}

func (o *MediaOrchestrator) okStates(allowed ...MediaState) error {
	for _, state := range allowed {
		if o.state == state {
			return nil
		}
	}
	return fmt.Errorf("invalid state: %s, expected one of %v: %w", o.state, allowed, ErrWrongState)
}

const DispatchTimeout = 20 * time.Second

func (o *MediaOrchestrator) dispatch(fn func() error) error {
	if !o.dispatchOK.Load() {
		return ErrWrongState
	}

	done := make(chan error)
	op := dispatchOperation{
		fn:   fn,
		done: done,
	}

	timeout := time.After(DispatchTimeout)

	select {
	case o.dispatchCH <- op:
		break
	case <-o.ctx.Done():
		return context.Canceled
	case <-timeout:
		o.log.Errorw("media orchestrator dispatch operation timed out", nil, "timeout", DispatchTimeout)
		return fmt.Errorf("media orchestrator dispatch operation timed out after %v: %w", DispatchTimeout, context.DeadlineExceeded)
	}

	select {
	case err := <-done:
		return err
	case <-o.ctx.Done():
		return context.Canceled
	case <-timeout:
		o.log.Errorw("media orchestrator dispatch operation timed out", nil, "timeout", DispatchTimeout)
		return fmt.Errorf("media orchestrator dispatch operation timed out after %v: %w", DispatchTimeout, context.DeadlineExceeded)
	}
}

func (o *MediaOrchestrator) dispatchLoop() {
	o.dispatchOK.Store(true)
	defer o.dispatchOK.Store(false)
	defer o.wg.Done()
	defer o.log.Debugw("media orchestrator dispatch loop exited")

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mu := sync.Mutex{}

	for {
		select {
		case <-o.ctx.Done():
			mu.Lock()
			o.log.Debugw("media orchestrator dispatch loop exiting")
			if err := o.close(); err != nil {
				o.log.Errorw("error closing media orchestrator", err)
			}
			mu.Unlock()
			return
		case op := <-o.dispatchCH:
			mu.Lock()
			err := op.fn()
			op.done <- err
			mu.Unlock()
		}
	}
}

func (o *MediaOrchestrator) close() error {
	var bfcpErr error
	// if o.bfcp != nil {
	// 	bfcpErr = o.bfcp.Close()
	// }
	err := errors.Join(
		o.pipeline.Close(),
		bfcpErr,
	)
	o.cancel()

	return err
}

func (o *MediaOrchestrator) Close() error {
	o.cancel()
	o.wg.Wait()

	log := o.log
	*o = MediaOrchestrator{}
	pipeline.ForceMemoryRelease()
	log.Debugw("media orchestrator closed")

	return nil
}

// GetRoom implements [RoomCallbacks].
func (o *MediaOrchestrator) JoinRoom(wsUrl, token string, callbacks *lksdk.RoomCallback, opts ...lksdk.ConnectOption) (*lksdk.Room, error) {
	var errs []error
	room, err := o.pipeline.GetRoom()
	if err != nil || room == nil {
		return nil, fmt.Errorf("could not get room from pipeline: %w", err)
	}
	errs = append(errs, err)
	errs = append(errs, o.pipeline.SetRoomCallbacks(callbacks))
	errs = append(errs, o.pipeline.SetRoomOptions(wsUrl, token, opts...))
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("could not join room: %w", err)
	}
	return room, nil
}

func (o *MediaOrchestrator) AnswerSDP(offer []byte) (answer []byte, err error) {
	if err := o.okStates(MediaStateFailed, MediaStateOK, MediaStateReady, MediaStateStarted); err != nil {
		return nil, err
	}
	if err := o.dispatch(func() error {
		answer, err = o.answerSDP(offer)
		return err
	}); err != nil {
		return nil, err
	}
	return answer, nil
}

func (o *MediaOrchestrator) answerSDP(offerData []byte) ([]byte, error) {
	res, err := o.pipeline.SipManager.Emit("on-remote-offer", string(offerData))
	if err != nil {
		o.log.Errorw("failed to emit on-remote-offer", err)
		return nil, fmt.Errorf("failed to emit on-remote-offer: %w", err)
	}
	answerStr, ok := res.(string)
	if !ok {
		o.log.Errorw("on-remote-offer did not return a string", nil, "value", res)
		return nil, fmt.Errorf("on-remote-offer did not return a string")
	}
	if answerStr == "" {
		o.log.Errorw("on-remote-offer returned an empty answer", nil)
		return nil, fmt.Errorf("on-remote-offer returned an empty answer")
	}

	o.state = MediaStateReady

	return []byte(answerStr), nil
}

// func (o *MediaOrchestrator) offerSDP(camera bool, bfcp bool, screenshare bool) (*sdpv2.SDP, error) {
// 	builder := (&sdpv2.SDP{}).Builder()

// 	builder.SetAddress(o.opts.IP)

// 	// audio is required anyway
// 	// builder.SetAudio(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
// 	// 	codec := o.audioinfo.Codec()
// 	// 	if codec == nil {
// 	// 		for _, c := range o.audioinfo.AvailableCodecs() {
// 	// 			b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
// 	// 				return c, nil
// 	// 			}, false)
// 	// 		}
// 	// 	} else {
// 	// 		b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
// 	// 			return codec, nil
// 	// 		}, true)
// 	// 	}
// 	// 	return b.
// 	// 		SetRTPPort(uint16(o.audioinfo.Port())).
// 	// 		Build()
// 	// }).Build()

// 	// if bfcp && o.bfcp != nil {
// 	// 	if screenshare {
// 	// 		builder.SetBFCP(func(b *sdpv2.SDPBfcpBuilder) (*sdpv2.SDPBfcp, error) {
// 	// 			return b.
// 	// 				SetPort(o.bfcp.Port()).
// 	// 				SetConnection(sdpv2.BfcpConnectionNew).
// 	// 				SetProto(sdpv2.BfcpProtoTCP).
// 	// 				SetFloorCtrl(sdpv2.BfcpFloorCtrlServer).
// 	// 				SetSetup(sdpv2.BfcpSetupPassive).
// 	// 				SetConfID(o.bfcp.config.ConferenceID).
// 	// 				SetUserID(1).
// 	// 				SetMStreamID(ScreenshareMSTreamID).
// 	// 				Build()
// 	// 		})
// 	// 	}
// 	// }

// 	// if camera {
// 	// 	builder.SetVideo(func(b *sdpv2.SDPMediaBuilder) (*sdpv2.SDPMedia, error) {
// 	// 		codec := o.video.Codec()
// 	// 		if codec == nil {
// 	// 			for _, c := range o.video.SupportedCodecs() {
// 	// 				b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
// 	// 					return c, nil
// 	// 				}, false)
// 	// 			}
// 	// 		} else {
// 	// 			b.AddCodec(func(_ *sdpv2.CodecBuilder) (*sdpv2.Codec, error) {
// 	// 				return codec, nil
// 	// 			}, true)
// 	// 		}
// 	// 		b.SetDisabled(o.video.Status() < VideoStatusReady)
// 	// 		// b.SetDisabled(false)
// 	// 		b.SetRTPPort(uint16(o.video.RtpPort()))
// 	// 		b.SetRTCPPort(uint16(o.video.RtcpPort()))
// 	// 		b.SetDirection(o.video.Direction())
// 	// 		return b.Build()
// 	// 	})
// 	// }

// 	// offer, err := builder.Build()
// 	// if err != nil {
// 	// 	return nil, fmt.Errorf("could create a new sdp: %w", err)
// 	// }
// 	// o.log.Debugw("created offer sdp", "offer", offer)

// 	return nil, nil
// }

// func (o *MediaOrchestrator) setupSDP(sdp *sdpv2.SDP) error {
// 	o.log.Debugw("setting up sdp", "sdp", sdp)

// 	o.log.Debugw("reconciling camera")
// 	if _, err := o.video.Reconcile(sdp.Addr, sdp.Video); err != nil {
// 		o.log.Errorw("could not reconcile video sdp", err)
// 		return fmt.Errorf("could not reconcile video sdp: %w", err)
// 	}
// 	return nil
// }

func (o *MediaOrchestrator) start() error {
	// if o.video.Status() == VideoStatusReady {
	// 	o.log.Debugw("starting camera")
	// 	if err := o.video.Start(); err != nil {
	// 		o.log.Errorw("could not start camera", err)
	// 		return fmt.Errorf("could not start camera: %w", err)
	// 	}
	// }

	o.state = MediaStateStarted
	return nil
}

func (o *MediaOrchestrator) Start() (err error) {
	if err := o.okStates(MediaStateReady); err != nil {
		return err
	}
	if err := o.dispatch(func() error {
		return o.start()
	}); err != nil {
		return err
	}
	return nil
}
