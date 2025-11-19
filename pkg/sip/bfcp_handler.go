package sip

import (
	"fmt"
	"net/netip"

	"github.com/livekit/protocol/logger"
	sdpv2 "github.com/livekit/media-sdk/sdp/v2"
	"github.com/livekit/media-sdk/sdp/v2/bfcp"
	bfcpclient "github.com/vopenia/bfcp"
)

type BFCPHandler struct {
	log logger.Logger

	serverAddr netip.Addr
	serverPort uint16

	negotiator   *bfcp.Negotiator
	offerConfig  *bfcp.Config
	answerConfig *bfcp.Config

	conferenceID  uint32
	userID        uint16
	enableLogging bool
}

func NewBFCPHandler(log logger.Logger, serverAddr string, serverPort int, enableLogging bool) (*BFCPHandler, error) {
	addr, err := netip.ParseAddr(serverAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid server address: %w", err)
	}

	return &BFCPHandler{
		log:           log,
		serverAddr:    addr,
		serverPort:    uint16(serverPort),
		enableLogging: enableLogging,
	}, nil
}

func (h *BFCPHandler) ProcessOffer(offer *sdpv2.SDP, conferenceID uint32) error {
	if offer == nil || offer.BFCP == nil {
		return fmt.Errorf("no BFCP in offer")
	}

	h.conferenceID = conferenceID
	h.negotiator = bfcp.NewNegotiator(h.serverAddr, h.serverPort)

	config, err := h.negotiator.ProcessOffer(offer)
	if err != nil {
		return fmt.Errorf("failed to process BFCP offer: %w", err)
	}

	h.offerConfig = config
	h.userID = config.UserID

	h.log.Infow("🔵 [BFCP-Handler] Processed BFCP offer",
		"vendor", h.negotiator.GetVendor(),
		"setup", config.SetupRole,
		"floorCtrl", config.FloorControl,
		"port", config.Port,
		"conferenceID", config.ConferenceID,
		"userID", config.UserID,
		"floorID", config.FloorID,
		"mediaStream", config.MediaStream,
	)

	return nil
}

func (h *BFCPHandler) CreateAnswer() (*sdpv2.BFCPMedia, error) {
	if h.offerConfig == nil {
		return nil, fmt.Errorf("no offer processed yet")
	}

	config, err := h.negotiator.CreateAnswer(h.offerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create BFCP answer: %w", err)
	}

	config.ConferenceID = h.conferenceID

	h.answerConfig = config

	h.log.Infow("🔵 [BFCP-Handler] Created BFCP answer",
		"setup", config.SetupRole,
		"floorCtrl", config.FloorControl,
		"conferenceID", config.ConferenceID,
		"userID", config.UserID,
		"content", config.Content,
	)

	return config.ToBFCPMedia(), nil
}

func (h *BFCPHandler) GetConfig() *bfcp.Config {
	return h.answerConfig
}

func (h *BFCPHandler) GetOfferUserID() uint16 {
	if h.offerConfig == nil {
		return 0
	}
	return h.offerConfig.UserID
}

func (h *BFCPHandler) GetConferenceID() uint32 {
	return h.conferenceID
}

func (h *BFCPHandler) CreateBFCPClient(webrtcUserID uint16) (*bfcpclient.Client, error) {
	if h.answerConfig == nil {
		return nil, fmt.Errorf("no answer config available")
	}

	serverAddr := fmt.Sprintf("%s:%d", h.serverAddr.String(), h.serverPort)

	clientConfig := &bfcpclient.ClientConfig{
		ServerAddress: serverAddr,
		ConferenceID:  h.conferenceID,
		UserID:        webrtcUserID,
		EnableLogging: h.enableLogging,
	}

	bfcpClient := bfcpclient.NewClient(clientConfig)

	h.log.Infow("🔵 [BFCP-Handler] Created BFCP client",
		"serverAddr", serverAddr,
		"conferenceID", h.conferenceID,
		"userID", webrtcUserID,
	)

	return bfcpClient, nil
}
