# Screen Share Implementation for WebRTC → SIP with BFCP

## Overview

This document describes the implementation of screen share support for WebRTC to SIP direction with BFCP (Binary Floor Control Protocol) floor control for Poly hardware devices.

## Architecture

### Components

1. **ScreenShareManager** ([pkg/sip/screenshare.go](pkg/sip/screenshare.go))
   - Manages screen share streams from WebRTC to SIP
   - Handles BFCP floor control for presentation management
   - Maintains dedicated GStreamer pipeline for screen share (VP8 → H264)
   - Manages separate RTP/RTCP connections for screen share stream

2. **Room Extensions** ([pkg/sip/room.go](pkg/sip/room.go))
   - Added `screenShareCallback` to detect screen share tracks
   - Detects screen share by checking `TrackSource == livekit.TrackSource_SCREEN_SHARE`
   - Separates screen share handling from regular camera video

3. **Inbound Call Integration** ([pkg/sip/inbound.go](pkg/sip/inbound.go))
   - Added `screenShare *ScreenShareManager` to `inboundCall` struct
   - Initializes screen share support when video is enabled
   - Manages screen share lifecycle (setup, start, stop, cleanup)
   - Placeholder for SIP re-INVITE implementation

## Implementation Flow

### 1. Call Setup
```
Inbound SIP Call
    ↓
SetupVideo() - Camera video pipeline
    ↓
SetupScreenShare() - Initialize screen share manager
    ↓
Register callback for screen share tracks
    ↓
Call proceeds normally
```

### 2. Screen Share Start
```
WebRTC participant starts screen share
    ↓
Room detects TrackSource_SCREEN_SHARE
    ↓
Invokes screenShareCallback
    ↓
ScreenShareManager.OnScreenShareTrack()
    ↓
Request BFCP floor
    ↓
Floor granted
    ↓
Start GStreamer pipeline (VP8 → H264)
    ↓
Trigger re-INVITE to add screen share stream
```

### 3. Screen Share Stop
```
WebRTC participant stops screen share
    ↓
ScreenShareManager.Stop()
    ↓
Stop GStreamer pipeline
    ↓
Release BFCP floor
    ↓
Trigger re-INVITE to remove screen share stream
```

## Key Features Implemented

### ✅ Completed

1. **Screen Share Detection**
   - Detects screen share tracks by `TrackSource` enum
   - Separates screen share from camera video
   - Debug logging with 🖥️ emoji for easy tracking

2. **Dedicated GStreamer Pipeline**
   - Separate pipeline optimized for presentation content
   - Higher bitrate (3000 kbps vs 2000 kbps for camera)
   - Better quality settings for text/graphics (qp-min=18, qp-max=36)
   - Longer keyframe interval (60 frames vs 30 for camera)
   - Medium preset for better quality vs camera's ultrafast

3. **BFCP Client Structure**
   - `BFCPClient` wrapper ready for integration
   - Floor request/grant/release methods
   - Callbacks for floor granted/revoked events
   - Placeholder for `/dev/bfcp` library integration

4. **Separate RTP/RTCP Ports**
   - Screen share uses independent port pair
   - Avoids conflicts with camera video stream
   - Proper RTCP monitoring and PLI handling

5. **Lifecycle Management**
   - Proper initialization in `SetupScreenShare()`
   - Cleanup in `closeMedia()`
   - State management with mutex protection

### ⏳ TODO (Next Steps)

1. **BFCP Integration**
   ```go
   // In screenshare.go, replace placeholder with:
   import "github.com/vopenia-io/bfcp"

   func (s *ScreenShareManager) connectBFCP(ctx context.Context) error {
       client := bfcp.NewClient(&bfcp.ClientConfig{
           ServerAddress:  s.bfcpClient.serverAddr,
           ConferenceID:   s.bfcpClient.conferenceID,
           UserID:         s.bfcpClient.userID,
           EnableLogging:  true,
       })

       // Set up callbacks
       client.OnFloorGranted = func(floorID, requestID uint16) {
           s.floorGranted = true
           if s.onFloorGranted != nil {
               s.onFloorGranted()
           }
       }

       client.OnFloorRevoked = func(floorID uint16) {
           s.floorGranted = false
           if s.onFloorRevoked != nil {
               s.onFloorRevoked()
           }
       }

       return client.Connect()
   }
   ```

2. **SDP with BFCP Attributes**
   - Parse BFCP attributes from incoming SDP offer
   - Add BFCP attributes to SDP answer:
     ```sdp
     m=application 5070 TCP/TLS/BFCP *
     a=setup:active
     a=connection:new
     a=floorid:1 mstrm:2
     a=confid:1
     a=userid:1
     a=floorctrl:c-only
     ```
   - Link BFCP stream to video m-line with `mstrm` attribute

3. **SIP re-INVITE Implementation**
   ```go
   func (c *inboundCall) sendScreenShareReInvite(addScreenShare bool) error {
       // Build new SDP
       offer, err := c.buildSDPWithScreenShare(addScreenShare)
       if err != nil {
           return err
       }

       // Send INVITE with new SDP
       inviteReq := sip.NewRequest(sip.INVITE, c.cc.To())
       inviteReq.SetBody(offer)
       inviteReq.AppendHeader(/* ... */)

       // Handle response
       resp, err := c.cc.SendRequest(inviteReq)
       if err != nil {
           return err
       }

       // Send ACK
       ack := sip.NewAckRequest(inviteReq, resp, nil)
       return c.cc.SendRequest(ack)
   }
   ```

4. **Dynamic SDP Building**
   - Extend `sdpv2.SDPBuilder` to support multiple video streams
   - Add screen share as second video m-line or use bundled approach
   - Handle port allocation for screen share stream

## Debug Logging

All screen share operations are logged with the 🖥️ emoji prefix for easy identification:

```
🖥️ [ScreenShare] Creating ScreenShareManager
🖥️ [ScreenShare] Screen share track detected
🖥️ [ScreenShare] Requesting presentation floor
🖥️ [ScreenShare] ✅ Presentation floor GRANTED
🖥️ [ScreenShare] GStreamer pipeline started
🖥️ [ScreenShare] Triggering re-INVITE to add screen share stream
```

## Testing Plan

### Unit Testing
1. Test screen share track detection
2. Test BFCP floor request/grant/release
3. Test GStreamer pipeline creation
4. Test lifecycle management (start/stop/cleanup)

### Integration Testing with Poly Device

1. **Setup**
   ```bash
   # Start livekit-sip server
   ./livekit-sip

   # Join LiveKit room from browser
   # Start screen share from browser
   ```

2. **Verify Camera Video Works**
   - Establish SIP call from Poly to livekit-sip
   - Verify camera video flows WebRTC → SIP
   - Check video quality on Poly device

3. **Test Screen Share Flow**
   - Start screen share from WebRTC participant
   - Check logs for: `🖥️ [ScreenShare] Screen share track detected`
   - Verify BFCP floor request: `🖥️ [ScreenShare] Requesting presentation floor`
   - Verify floor granted: `🖥️ [ScreenShare] ✅ Presentation floor GRANTED`
   - Verify re-INVITE sent: `🖥️ [ScreenShare] Triggering re-INVITE`
   - Check Poly device shows presentation/content view
   - Verify screen share video quality

4. **Test Screen Share Stop**
   - Stop screen share from WebRTC participant
   - Check logs for: `🖥️ [ScreenShare] Stopping screen share`
   - Verify floor released: `🖥️ [ScreenShare] Presentation floor RELEASED`
   - Verify re-INVITE sent to remove stream
   - Check Poly device returns to camera view

5. **Test Edge Cases**
   - Multiple screen share attempts (only one should be active)
   - Screen share without camera video
   - Call disconnect while screen sharing
   - Network interruption during screen share

## Configuration

### Environment Variables
```bash
# BFCP server configuration (extracted from SDP in production)
BFCP_SERVER_ADDR=192.168.0.10:5070
BFCP_CONFERENCE_ID=1
BFCP_USER_ID=1
BFCP_FLOOR_ID=1  # Typically 1 for presentation
```

### GStreamer Pipeline Parameters
- **Bitrate**: 3000 kbps (higher than camera for better quality)
- **Keyframe Interval**: 60 frames (2 seconds at 30fps)
- **Quality**: qp-min=18, qp-max=36 (better than camera)
- **Preset**: medium (vs ultrafast for camera)
- **Tune**: zerolatency (same as camera)

## Files Modified/Created

### Created
- [pkg/sip/screenshare.go](pkg/sip/screenshare.go) - Main screen share manager implementation (565 lines)

### Modified
- [pkg/sip/room.go](pkg/sip/room.go)
  - Added `screenShareCallback` field
  - Added `SetScreenShareCallback()` method
  - Modified `participantVideoTrackSubscribed()` to detect screen share

- [pkg/sip/inbound.go](pkg/sip/inbound.go)
  - Added `screenShare` field to `inboundCall` struct
  - Added `SetupScreenShare()` method
  - Added `sendScreenShareReInvite()` method (placeholder)
  - Modified `runMediaConn()` to initialize screen share
  - Modified `closeMedia()` to cleanup screen share

## Next Steps for Production

1. **Complete BFCP Integration**
   - Replace placeholder BFCP client with actual `/dev/bfcp` library
   - Extract BFCP parameters from SDP attributes
   - Handle BFCP errors and retries

2. **Implement SIP re-INVITE**
   - Build SDP with multiple video streams
   - Send re-INVITE via SIP stack
   - Handle 200 OK and ACK properly

3. **Add BFCP SDP Attributes**
   - Parse incoming BFCP attributes from SIP device
   - Generate BFCP attributes in SDP answer
   - Link BFCP floor to video stream

4. **Testing**
   - Test with real Poly device
   - Verify BFCP floor control works
   - Test various screen share scenarios
   - Performance testing with high-resolution screen share

5. **Error Handling**
   - Handle BFCP floor deny/revoke scenarios
   - Graceful fallback if BFCP not supported
   - Retry logic for transient failures

6. **Monitoring**
   - Add metrics for screen share sessions
   - Track BFCP floor request success rate
   - Monitor screen share video quality
   - Alert on screen share failures

## References

- BFCP RFC 8855: https://www.rfc-editor.org/rfc/rfc8855.html
- BFCP SDP RFC 8856: https://www.rfc-editor.org/rfc/rfc8856.html
- BFCP Library: /home/vopenia/dev/bfcp
- LiveKit Protocol: https://github.com/livekit/protocol
- SIP RFC 3261: https://www.rfc-editor.org/rfc/rfc3261.html

## Summary

This implementation provides a solid foundation for screen share support from WebRTC to SIP with BFCP floor control. The architecture is clean, with proper separation of concerns:

- **ScreenShareManager**: Dedicated manager for screen share streams
- **Separate GStreamer Pipeline**: Optimized for presentation content
- **BFCP Client**: Floor control for Poly devices
- **Debug Logging**: Comprehensive logging for troubleshooting

The main work remaining is:
1. Integration with actual BFCP library
2. SIP re-INVITE implementation
3. SDP attribute handling
4. End-to-end testing with Poly device

All code compiles successfully and follows the existing patterns in the codebase.
