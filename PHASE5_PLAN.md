# Phase 5: Outbound re-INVITE Implementation Plan

## Goal
Send a SIP re-INVITE from LiveKit SIP Gateway → SIP Device to add screen share video stream + BFCP floor control.

## Critical Success Factors
1. **Video must continue working** - Camera video must not be disrupted
2. **Proper state management** - CSeq tracking, transaction lifecycle
3. **Timing** - Wait for BFCP grant before sending media
4. **Testable at each step** - User can test after each phase

---

## Implementation Phases

### Phase 5.1: Add Transaction Method (SAFE - No SIP traffic yet)
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Add method to `sipInbound` to create client transactions:
```go
func (c *sipInbound) Transaction(req *sip.Request) (sip.ClientTransaction, error) {
	return c.s.sipSrv.Request(req)
}
```

**Test:** Compile only - no runtime changes

---

### Phase 5.2: Build Current SDP State (SAFE - No SIP traffic yet)
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Create helper to build SDP from current state:
```go
func (c *inboundCall) buildCurrentSDP() (*sdpv2.SDP, error) {
	// Build SDP with current audio + video state
	// NO screen share yet, NO BFCP yet
}
```

**Test:** Log the generated SDP - verify it matches initial answer

---

### Phase 5.3: Add Screen Share Video M-Line (SAFE - No SIP traffic yet)
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Extend `buildCurrentSDP` to optionally add screen share:
```go
func (c *inboundCall) buildReInviteSDP(includeScreenShare bool) (*sdpv2.SDP, error) {
	sdp := ... // build base SDP

	if includeScreenShare && c.screenShare != nil {
		// Add second video m-line with content:slides
	}

	return sdp, nil
}
```

**Test:** Log SDP with screen share - verify format

---

### Phase 5.4: Add BFCP M-Line (SAFE - No SIP traffic yet)
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Add BFCP to re-INVITE SDP:
```go
if includeScreenShare {
	sdp.BFCP = &sdpv2.BFCPMedia{
		Port:         uint16(conf.BFCPPort),
		ConnectionIP: c.s.sconf.MediaIP,
		FloorCtrl:    "s-only",  // We are BFCP server
		ConferenceID: 1,
		UserID:       2,  // Assign unique ID
		FloorID:      2,  // Floor 2 for WebRTC→SIP
		MediaStream:  2,  // Links to screen share video (m-line index 2)
		Setup:        "passive",
		Connection:   "new",
	}
}
```

**Test:** Log full SDP - verify BFCP parameters

---

### Phase 5.5: Send re-INVITE (⚠️ FIRST SIP TRANSACTION)
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Implement `sendScreenShareReInvite`:
```go
func (c *inboundCall) sendScreenShareReInvite(addScreenShare bool) error {
	c.log.Infow("🖥️ [re-INVITE] Building SDP", "addScreenShare", addScreenShare)

	// Step 1: Build SDP
	sdp, err := c.buildReInviteSDP(addScreenShare)
	if err != nil {
		return err
	}

	sdpBytes, err := sdp.Marshal()
	if err != nil {
		return err
	}

	c.log.Infow("🖥️ [re-INVITE] SDP built", "size", len(sdpBytes))

	// Step 2: Create INVITE request
	req := sip.NewRequest(sip.INVITE, c.cc.To().Address)
	req.SetBody(sdpBytes)

	// Step 3: Add headers (copy from initial INVITE)
	// ... (detailed in implementation)

	// Step 4: Set CSeq (incrementing)
	c.cc.setCSeq(req)

	// Step 5: Swap src/dst (we are client now)
	c.cc.swapSrcDst(req)

	c.log.Infow("🖥️ [re-INVITE] Sending INVITE", "cseq", c.cc.nextRequestCSeq)

	// Step 6: Create transaction
	tx, err := c.cc.Transaction(req)
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}
	defer tx.Terminate()

	c.log.Infow("🖥️ [re-INVITE] Transaction created, waiting for response...")

	// Step 7: Wait for response (with timeout)
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	resp, err := c.waitForResponse(ctx, tx)
	if err != nil {
		return err
	}

	c.log.Infow("🖥️ [re-INVITE] Received response", "status", resp.StatusCode)

	// Step 8: Handle response
	return c.handleReInviteResponse(req, resp, addScreenShare)
}
```

**Test:** Send re-INVITE with `addScreenShare=false` (no BFCP, no screen share)
- Verify SIP device responds with 200 OK
- Verify camera video still works
- Log any errors

---

### Phase 5.6: Wait for 200 OK Response
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Handle transaction responses:
```go
func (c *inboundCall) waitForResponse(ctx context.Context, tx sip.ClientTransaction) (*sip.Response, error) {
	for {
		select {
		case <-ctx.Done():
			tx.Cancel()
			return nil, fmt.Errorf("timeout waiting for response")
		case <-tx.Done():
			return nil, fmt.Errorf("transaction failed")
		case resp := <-tx.Responses():
			if resp.StatusCode >= 200 {
				return resp, nil  // Final response
			}
			// Log 1xx provisional responses
			c.log.Infow("🖥️ [re-INVITE] Provisional response", "status", resp.StatusCode)
		}
	}
}
```

**Test:** Verify we receive 200 OK within timeout

---

### Phase 5.7: Parse SDP Answer
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Parse SDP from 200 OK:
```go
func (c *inboundCall) handleReInviteResponse(req *sip.Request, resp *sip.Response, addScreenShare bool) error {
	if resp.StatusCode != 200 {
		return fmt.Errorf("re-INVITE failed: %d %s", resp.StatusCode, resp.Reason)
	}

	c.log.Infow("🖥️ [re-INVITE] Parsing SDP answer")

	// Parse SDP
	sdp, err := sdpv2.Parse(resp.Body(), c.s.sconf.MediaIP)
	if err != nil {
		return fmt.Errorf("failed to parse SDP: %w", err)
	}

	c.log.Infow("🖥️ [re-INVITE] SDP parsed",
		"hasAudio", sdp.Audio != nil,
		"hasVideo", sdp.Video != nil,
		"hasBFCP", sdp.BFCP != nil)

	// Update connections
	if addScreenShare {
		return c.updateScreenShareConnections(sdp)
	}

	// Send ACK
	return c.sendReInviteACK(req, resp)
}
```

**Test:** Log parsed SDP - verify fields

---

### Phase 5.8: Update Screen Share Connections
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Configure RTP/RTCP destinations:
```go
func (c *inboundCall) updateScreenShareConnections(sdp *sdpv2.SDP) error {
	if sdp.Video == nil {
		return fmt.Errorf("no video in SDP answer")
	}

	c.log.Infow("🖥️ [re-INVITE] Updating screen share connections",
		"remote", sdp.Addr,
		"rtpPort", sdp.Video.Port,
		"rtcpPort", sdp.Video.RTCPPort)

	// Update screen share manager with remote ports
	if c.screenShare != nil {
		rtpAddr := netip.AddrPortFrom(sdp.Addr, sdp.Video.Port)
		rtcpAddr := netip.AddrPortFrom(sdp.Addr, sdp.Video.RTCPPort)

		c.screenShare.rtpConn.SetDst(rtpAddr)
		c.screenShare.rtcpConn.SetDst(rtcpAddr)

		c.log.Infow("🖥️ [re-INVITE] ✅ Screen share connections updated")
	}

	return nil
}
```

**Test:** Log connection updates - verify ports

---

### Phase 5.9: Send ACK
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Complete transaction:
```go
func (c *inboundCall) sendReInviteACK(req *sip.Request, resp *sip.Response) error {
	ack := sip.NewAckRequest(req, resp, nil)

	c.log.Infow("🖥️ [re-INVITE] Sending ACK")

	if err := c.cc.WriteRequest(ack); err != nil {
		return fmt.Errorf("failed to send ACK: %w", err)
	}

	c.log.Infow("🖥️ [re-INVITE] ✅ ACK sent - transaction complete")
	return nil
}
```

**Test:** Verify ACK is sent (check SIP logs)

---

### Phase 5.10: Setup BFCP Client Connection
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

Connect to BFCP server AFTER SDP negotiation:
```go
// In handleReInviteResponse, after updateScreenShareConnections:
if addScreenShare && sdp.BFCP != nil {
	c.log.Infow("🖥️ [re-INVITE] Setting up BFCP client")

	bfcpAddr := fmt.Sprintf("%s:%d", sdp.BFCP.ConnectionIP, sdp.BFCP.Port)

	if err := c.screenShare.SetupBFCP(
		c.ctx,
		bfcpAddr,
		sdp.BFCP.ConferenceID,
		sdp.BFCP.UserID,
		sdp.BFCP.FloorID,
	); err != nil {
		c.log.Errorw("🖥️ [re-INVITE] ⚠️ BFCP setup failed", err)
		// Don't fail re-INVITE - screen share might still work without floor control
	}
}
```

**Test:** Verify BFCP TCP connection established

---

### Phase 5.11: Request Floor Grant
**File:** `/home/vopenia/dev/livekit-sip/pkg/sip/screenshare.go`

This is already implemented in `startLocked()` at lines 446-477!

**Test:** Verify floor request sent and grant received

---

### Phase 5.12: Verify Screen Share RTP Flows
**Integration Test**

1. WebRTC client publishes screen share
2. `OnScreenShareTrack()` triggers (already working)
3. `startLocked()` calls `onStartCallback()`
4. `sendScreenShareReInvite(true)` executes
5. Re-INVITE sent → 200 OK received → ACK sent
6. BFCP client connects → Floor requested → Floor granted
7. GStreamer pipeline converts VP8 → H264
8. RTP packets flow to SIP device

**Test checklist:**
- [ ] Camera video still works
- [ ] Screen share video appears on SIP device
- [ ] BFCP logs show floor granted
- [ ] RTP stats show packets flowing
- [ ] No crashes or hangs

---

## State Diagram

```
Initial Call (Camera Video)
         |
         | WebRTC publishes screen share
         v
    [OnScreenShareTrack]
         |
         | startLocked() → onStartCallback()
         v
    [sendScreenShareReInvite(true)]
         |
         ├─> Build SDP (audio + camera + screen share + BFCP)
         ├─> Send INVITE
         ├─> Wait 200 OK
         ├─> Parse SDP answer
         ├─> Update RTP destinations
         ├─> Send ACK
         └─> Setup BFCP → Request Floor → **SCREEN SHARE ACTIVE**
```

---

## Risk Mitigation

### Risk 1: Camera video breaks during re-INVITE
**Mitigation:** Test with `addScreenShare=false` first (no new media, just refresh)

### Risk 2: SIP device rejects re-INVITE
**Mitigation:** Log full request/response, check SDP format matches initial INVITE

### Risk 3: Transaction timeout
**Mitigation:** 30-second timeout, proper cleanup on failure

### Risk 4: BFCP connection fails
**Mitigation:** Non-fatal - screen share might work without floor control

### Risk 5: State corruption
**Mitigation:** Store original `inviteOk` separately, don't overwrite until successful

---

## Testing Strategy

### Test 1: No-op re-INVITE (Phase 5.5)
- Send re-INVITE with same SDP as initial answer
- Expected: 200 OK, camera video continues

### Test 2: Add screen share video only (Phase 5.8)
- Send re-INVITE with screen share m-line, NO BFCP
- Expected: 200 OK, camera + screen share video

### Test 3: Full implementation (Phase 5.12)
- Send re-INVITE with screen share + BFCP
- Expected: 200 OK, BFCP grant, screen share RTP flows

---

## Next Steps

1. Review this plan
2. Confirm approach
3. Implement Phase 5.1-5.4 (no SIP traffic - safe)
4. Test each phase incrementally
5. User tests after each phase

**Ready to proceed?**
