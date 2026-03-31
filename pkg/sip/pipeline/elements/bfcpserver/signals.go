package bfcpserver

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

func (e *BFCPServer) SetupSignals(self *gst.Element) {
	e.bfcpServer.OnFloorGranted = func(floorID, userID, requestID uint16) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor granted: floorID=%d, userID=%d, requestID=%d", floorID, userID, requestID))
		if _, err := self.Emit("on-floor-granted", int(floorID), int(userID), int(requestID)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting on-floor-granted signal: %v", err))
			self.Error("Error emitting on-floor-granted signal", err)
		}
	}

	e.bfcpServer.OnFloorReleased = func(floorID, userID uint16) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Floor released: floorID=%d, userID=%d", floorID, userID))
		if _, err := self.Emit("on-floor-released", int(floorID), int(userID)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error emitting on-floor-released signal: %v", err))
			self.Error("Error emitting on-floor-released signal", err)
		}
	}
}
