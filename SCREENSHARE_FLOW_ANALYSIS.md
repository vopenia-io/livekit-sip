# Screen Share Flow Analysis - BFCP Exchange & SDP Timing

## Overview
This document analyzes the complete flow of WebRTC → SIP screen sharing with BFCP floor control, including timing, state transitions, and all SDP exchanges.

## Network Topology
- **SIP Device**: 192.168.0.104 (Polycom/Cisco endpoint)
- **LiveKit SIP Gateway**: 192.168.0.10
- **BFCP Server**: 192.168.0.10:5070
- **Transport**: TCP (SIP + BFCP)

---

## Phase 1: Initial Call Setup (WORKING ✓)

### Timeline: T+0s - Initial INVITE from SIP Device

**SIP Message Flow:**
```
SIP Device (192.168.0.104) → LiveKit (192.168.0.10)
TCP Connection 1 (Inbound)

INVITE sip:target@192.168.0.10 SIP/2.0
From: <sip:+14255551234@192.168.0.104>;tag=ABC123
To: <sip:target@192.168.0.10>
Call-ID: unique-call-id-12345
CSeq: 1 INVITE
Contact: <sip:192.168.0.104:5060;transport=tcp>
Content-Type: application/sdp

[SDP Offer]
```

**SDP Offer from SIP Device:**
```sdp
v=0
o=- 123456789 123456789 IN IP4 192.168.0.104
s=SIP Call
c=IN IP4 192.168.0.104
t=0 0

m=audio 20000 RTP/AVP 9 0
a=rtpmap:9 G722/8000
a=rtpmap:0 PCMU/8000
a=sendrecv

m=video 20002 RTP/AVP 109 96
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020
a=rtcp:20003
a=sendrecv
a=content:main

m=application 5060 TCP/BFCP *
c=IN IP4 192.168.0.104
a=setup:active
a=connection:new
a=floorctrl:c-s role:c-s
a=confid:1
a=userid:2
a=floorid:1 m-stream:2
```

**BFCP Analysis:**
- **Role**: `c-s` (client-server) - SIP device wants to be BOTH client AND server
- **Setup**: `active` - SIP device will initiate TCP connection
- **ConferenceID**: 1
- **UserID**: 2
- **FloorID**: 1 (linked to video m-line index 2)
- **Connection**: `new` (first BFCP setup)

**State: SIP Device wants BFCP for screen share control**

---

### Timeline: T+0.1s - 200 OK from LiveKit

**SIP Response:**
```
200 OK
From: <sip:+14255551234@192.168.0.104>;tag=ABC123
To: <sip:target@192.168.0.10>;tag=XYZ789
Call-ID: unique-call-id-12345
CSeq: 1 INVITE
Contact: <sip:192.168.0.10:5060;transport=tcp>
Content-Type: application/sdp

[SDP Answer]
```

**SDP Answer from LiveKit (Current - Phase 4):**
```sdp
v=0
o=- 16805185217773373789 16805185217773373789 IN IP4 192.168.0.10
s=LiveKit
c=IN IP4 192.168.0.10
t=0 0

m=audio 14508 RTP/AVP 9
a=rtpmap:9 G722/8000
a=ptime:20
a=sendrecv

m=video 18026 RTP/AVP 109
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020; max-mbps=490000; max-fs=8192
a=rtcp:18027
a=ptime:20
a=sendrecv

(NO BFCP m-line in answer - deferred to re-INVITE)
```

**Key Decision:** LiveKit **rejects BFCP in initial answer**, stores BFCP params for later re-INVITE.

**State Changes:**
- **Dialog State**: ESTABLISHED (From-tag, To-tag, Call-ID locked)
- **TCP Connection**: Connection 1 (inbound from SIP device) - **THIS IS CRITICAL**
- **BFCP State**: NOT STARTED (no BFCP exchange yet)
- **Media State**: Audio + Camera Video flowing

---

### Timeline: T+0.2s - ACK from SIP Device

**SIP Message:**
```
ACK sip:192.168.0.10:5060 SIP/2.0
From: <sip:+14255551234@192.168.0.104>;tag=ABC123
To: <sip:target@192.168.0.10>;tag=XYZ789
Call-ID: unique-call-id-12345
CSeq: 1 ACK
```

**State:** Call established, audio + camera video working ✓

---

## Phase 2: WebRTC Screen Share Published (T+30s)

### Timeline: T+30s - WebRTC Client Publishes Screen Share

**Event:** WebRTC participant in LiveKit room publishes screen share track

**Code Path:**
```
pkg/sip/inbound.go:1081
OnScreenShareTrack() → screenShareManager.OnScreenShareTrack(track, codec)
  ↓
pkg/sip/screenshare.go:414
startLocked()
  ↓ Create GStreamer pipeline (VP8 → H264)
  ↓ Create UDP RTP/RTCP sockets (unconnected yet)
  ↓ Call onStartCallback() → sendScreenShareReInvite(true)
```

**Screen Share Manager State:**
- **Pipeline**: GStreamer running (VP8 decoder → H264 encoder)
- **RTP Socket**: Created on port 31948 (example)
- **RTCP Socket**: Created on port 31949 (example)
- **Destination**: NOT SET (waiting for SDP answer with remote ports)
- **BFCP State**: NOT STARTED (waiting for negotiation)

**Log Output:**
```
INFO  🖥️ [ScreenShare] VP8 -> H264 transcoding pipeline created
INFO  🖥️ [ScreenShare] RTP socket created (local: 192.168.0.10:31948)
INFO  🖥️ [ScreenShare] RTCP socket created (local: 192.168.0.10:31949)
INFO  🖥️ [Inbound] Screen share started - re-INVITE needed
```

---

## Phase 3: Outbound re-INVITE Attempt (FAILING ✗)

### Timeline: T+30.1s - Build re-INVITE SDP

**Code Path:**
```
pkg/sip/inbound.go:1150
buildReInviteSDP(includeScreenShare=true)
  ↓ Clone initialAnswer
  ↓ Add screen share video m-line (TODO - currently placeholder)
  ↓ Add BFCP m-line (TODO - currently placeholder)
```

**Current SDP (Phase 5.2-5.4 Placeholders):**
```sdp
v=0
o=- 16805185217773373789 16805185217773373789 IN IP4 192.168.0.10
s=LiveKit
c=IN IP4 192.168.0.10
t=0 0

m=audio 14508 RTP/AVP 9
a=rtpmap:9 G722/8000
a=ptime:20
a=sendrecv

m=video 18026 RTP/AVP 109
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020; max-mbps=490000; max-fs=8192
a=rtcp:18027
a=ptime:20
a=sendrecv

(TODO: Add screen share video m-line)
(TODO: Add BFCP m-line)
```

**Expected SDP (Phase 5.3-5.4 Complete):**
```sdp
v=0
o=- 16805185217773373789 16805185217773373790 IN IP4 192.168.0.10
s=LiveKit
c=IN IP4 192.168.0.10
t=0 0

m=audio 14508 RTP/AVP 9
a=rtpmap:9 G722/8000
a=ptime:20
a=sendrecv

m=video 18026 RTP/AVP 109
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020; max-mbps=490000; max-fs=8192
a=rtcp:18027
a=ptime:20
a=sendrecv
a=content:main

m=video 31948 RTP/AVP 109
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020; max-mbps=490000; max-fs=8192
a=rtcp:31949
a=ptime:20
a=sendrecv
a=content:slides
a=label:screenshare

m=application 5070 TCP/BFCP *
c=IN IP4 192.168.0.10
a=setup:passive
a=connection:new
a=floorctrl:s-only
a=confid:1
a=userid:2
a=floorid:2 m-stream:3
```

**BFCP Parameters Explained:**
- **FloorCtrl**: `s-only` (server-only) - LiveKit is BFCP server, SIP device is client
- **Setup**: `passive` - LiveKit listens on port 5070
- **FloorID**: 2 (new floor for WebRTC→SIP screen share)
- **m-stream**: 3 (links to screen share video m-line index 3)

---

### Timeline: T+30.2s - Send re-INVITE (CURRENT FAILURE POINT)

**SIP Message Attempt:**
```
INVITE sip:192.168.0.104:5060 SIP/2.0
Via: SIP/2.0/TCP 192.168.0.10:5060;branch=z9hG4bK-NEW-BRANCH
From: <sip:target@192.168.0.10>;tag=XYZ789
To: <sip:+14255551234@192.168.0.104>;tag=ABC123
Call-ID: unique-call-id-12345
CSeq: 2 INVITE
Contact: <sip:192.168.0.10:5060;transport=tcp>
Max-Forwards: 70
Content-Type: application/sdp

[SDP Offer - screen share + BFCP]
```

**Problem - TCP Connection Issue:**
```
Code: c.cc.Transaction(req)
Result: Creates NEW outbound TCP connection (Connection 2)
Expected: Should use existing inbound connection (Connection 1)
```

**Connection Diagram:**
```
INITIAL INVITE (Working):
SIP Device --[Connection 1: TCP Inbound]--> LiveKit
   From-tag: ABC123
   To-tag: XYZ789

RE-INVITE ATTEMPT (Failing):
LiveKit --[Connection 2: NEW TCP Outbound]--> SIP Device
   From-tag: XYZ789 (swapped - correct)
   To-tag: ABC123 (swapped - correct)

Problem: SIP Device expects re-INVITE on Connection 1, receives it on Connection 2
```

---

### Timeline: T+30.3s - 481 Error Response

**SIP Response:**
```
481 Call/Transaction Does Not Exist
From: <sip:target@192.168.0.10>;tag=XYZ789
To: <sip:+14255551234@192.168.0.104>;tag=ABC123
Call-ID: unique-call-id-12345
CSeq: 2 INVITE
```

**Root Cause Analysis:**

1. **Dialog Matching (Headers):** ✓ CORRECT
   - Call-ID matches: unique-call-id-12345
   - From-tag matches: XYZ789 (our tag from 200 OK)
   - To-tag matches: ABC123 (their tag from INVITE)

2. **TCP Connection (Transport):** ✗ WRONG
   - SIP device received re-INVITE on **new connection**
   - Expected re-INVITE on **original dialog connection**
   - RFC 3261 §12.2.1.1: "Requests within a dialog MAY contain Record-Route and MUST be sent to the appropriate destination"

**State:** Screen share blocked - cannot proceed to BFCP negotiation

**Log Output:**
```
ERROR 🖥️ [re-INVITE] Request failed (status: 481, reason: "Call/Transaction Does Not Exist")
ERROR 🖥️ [ScreenShare] ❌ re-INVITE callback failed (error: "re-INVITE failed: 481 Call/Transaction Does Not Exist")
```

---

## Phase 4: Inbound re-INVITE (WORKING ✓) - Comparison

### Timeline: T+45s - SIP Device Adds/Removes Camera Video

**Event:** User presses "Start Video" / "Stop Video" on SIP device

**SIP Message Flow:**
```
SIP Device --[Connection 1: Original TCP]--> LiveKit

INVITE sip:192.168.0.10:5060 SIP/2.0
From: <sip:+14255551234@192.168.0.104>;tag=ABC123
To: <sip:target@192.168.0.10>;tag=XYZ789
Call-ID: unique-call-id-12345
CSeq: 3 INVITE
Content-Type: application/sdp

[SDP with video added/removed]
```

**Key Difference:**
- **Connection**: Uses **original inbound TCP connection (Connection 1)** ✓
- **Result**: LiveKit accepts re-INVITE, updates media ✓
- **User Feedback**: "great step Start/stop video is Working !!!"

**Code Path:**
```
pkg/sip/inbound.go:1768
onRequest() → handleInviteRequest()
  ↓ Received on existing connection
  ↓ Parse SDP, update media ports
  ↓ Send 200 OK on same connection
  ↓ Media updated successfully
```

**Why This Works:**
- SIP device **correctly** sends re-INVITE on the same TCP connection as original INVITE
- LiveKit ServerTransaction receives it on correct connection
- Dialog matching succeeds + connection matches

---

## Expected Flow (Phase 5 Complete - NOT REACHED YET)

### Timeline: T+30.3s - 200 OK to re-INVITE (Expected)

**Expected SIP Response:**
```
200 OK
From: <sip:target@192.168.0.10>;tag=XYZ789
To: <sip:+14255551234@192.168.0.104>;tag=ABC123
Call-ID: unique-call-id-12345
CSeq: 2 INVITE
Contact: <sip:192.168.0.104:5060;transport=tcp>
Content-Type: application/sdp

[SDP Answer - accepts screen share + BFCP]
```

**Expected SDP Answer:**
```sdp
v=0
o=- 123456789 123456790 IN IP4 192.168.0.104
s=SIP Call
c=IN IP4 192.168.0.104
t=0 0

m=audio 20000 RTP/AVP 9
a=rtpmap:9 G722/8000
a=sendrecv

m=video 20002 RTP/AVP 109
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020
a=rtcp:20003
a=sendrecv
a=content:main

m=video 20004 RTP/AVP 109
a=rtpmap:109 H264/90000
a=fmtp:109 profile-level-id=428020
a=rtcp:20005
a=sendrecv
a=content:slides

m=application 5070 TCP/BFCP *
c=IN IP4 192.168.0.10
a=setup:active
a=connection:new
a=floorctrl:s-only
a=confid:1
a=userid:2
a=floorid:2 m-stream:3
```

**Expected Actions:**
```
handleReInviteResponse()
  ↓ Parse SDP answer
  ↓ Update screen share RTP destinations
  ↓   rtpConn.SetDst(192.168.0.104:20004)
  ↓   rtcpConn.SetDst(192.168.0.104:20005)
  ↓ Send ACK
  ↓ Setup BFCP client connection
```

---

### Timeline: T+30.4s - ACK (Expected)

**Expected SIP Message:**
```
ACK sip:192.168.0.104:5060 SIP/2.0
From: <sip:target@192.168.0.10>;tag=XYZ789
To: <sip:+14255551234@192.168.0.104>;tag=ABC123
Call-ID: unique-call-id-12345
CSeq: 2 ACK
```

**State:** SDP negotiation complete, media destinations set

---

### Timeline: T+30.5s - BFCP TCP Connection (Expected)

**BFCP Flow:**
```
SIP Device (192.168.0.104) --[TCP Connect]--> LiveKit BFCP Server (192.168.0.10:5070)
```

**BFCP Server State:**
- **Listener**: tcp://192.168.0.10:5070 (started at gateway startup)
- **Conference ID**: 1
- **Floor ID**: 2 (for WebRTC→SIP screen share)
- **Auto-Grant**: true (server configured to auto-grant requests)

**Code:**
```go
// pkg/sip/server.go - BFCP server already running
bfcpServer := bfcp.NewServer(&bfcp.ServerConfig{
    Address:        fmt.Sprintf(":%d", conf.BFCPPort),
    ConferenceID:   1,
    AutoGrant:      true,
    EnableLogging:  true,
})
```

---

### Timeline: T+30.6s - BFCP Hello Exchange (Expected)

**BFCP Message 1: Hello from SIP Device**
```
Primitive: Hello (5)
ConferenceID: 1
TransactionID: 1001
UserID: 2

Attributes:
  SUPPORTED-PRIMITIVES: [1,2,3,4,5,6,13,14]
  SUPPORTED-ATTRIBUTES: [1,2,3,4,5,9,10]
```

**BFCP Message 2: HelloAck from LiveKit**
```
Primitive: HelloAck (6)
ConferenceID: 1
TransactionID: 1001
UserID: 2

Attributes:
  SUPPORTED-PRIMITIVES: [1,2,3,4,5,6,13,14]
  SUPPORTED-ATTRIBUTES: [1,2,3,4,5,9,10]
```

**State:** BFCP session established

---

### Timeline: T+30.7s - BFCP Floor Request (Expected)

**BFCP Message 3: FloorRequest from SIP Device**
```
Primitive: FloorRequest (1)
ConferenceID: 1
TransactionID: 1002
UserID: 2

Attributes:
  FLOOR-ID: 2
  BENEFICIARY-ID: 2 (optional - defaults to UserID)
  PRIORITY: Normal (32768)
```

**Server Logic:**
```go
// pkg/sip/server.go - OnFloorRequest callback
floor.Request(userID=2, requestID=1, priority=Normal)
  ↓ Check if floor available
  ↓ AutoGrant=true → Grant immediately
```

**BFCP Message 4: FloorRequestStatus (Pending)**
```
Primitive: FloorRequestStatus (3)
ConferenceID: 1
TransactionID: 1002
UserID: 2

Attributes:
  FLOOR-ID: 2
  FLOOR-REQUEST-ID: 1
  REQUEST-STATUS: Pending (1), QueuePos: 0
```

---

### Timeline: T+30.8s - BFCP Floor Granted (Expected)

**BFCP Message 5: FloorRequestStatus (Granted)**
```
Primitive: FloorRequestStatus (3)
ConferenceID: 1
TransactionID: 1003
UserID: 2

Attributes:
  FLOOR-ID: 2
  FLOOR-REQUEST-ID: 1
  REQUEST-STATUS: Granted (2), QueuePos: 0
```

**State Changes:**
- **BFCP Floor State**: GRANTED ✓
- **Screen Share State**: ACTIVE - ready to send RTP
- **GStreamer Pipeline**: Starts encoding H264
- **RTP Flow**: Begins immediately after grant

**Expected Log Output:**
```
INFO  [BFCP Server] Floor 2 granted to user 2 (request 1)
INFO  🖥️ [ScreenShare] ✅ Floor granted - starting RTP
```

---

### Timeline: T+30.9s - Screen Share RTP Flow (Expected)

**RTP Packets:**
```
LiveKit (192.168.0.10:31948) → SIP Device (192.168.0.104:20004)

RTP Header:
  Payload Type: 109 (H264)
  Sequence: 1, 2, 3, ...
  Timestamp: 90000 Hz
  SSRC: Random

RTCP Sender Reports:
LiveKit (192.168.0.10:31949) → SIP Device (192.168.0.104:20005)
```

**GStreamer Pipeline:**
```
VP8 RTP → rtpvp8depay → vp8dec → videoscale → videoconvert
  → x264enc → h264parse → rtph264pay → UDP sink
```

**Expected Result:**
- Screen share video appears on SIP device display ✓
- BFCP floor control active ✓
- Camera video continues uninterrupted ✓

---

## State Machine Summary

### Dialog State
```
INITIAL INVITE → ESTABLISHED (Connection 1: Inbound TCP)
  ↓
RE-INVITE (screen share) → BLOCKED (481 error - wrong connection)
  ↓
EXPECTED: ESTABLISHED (updated SDP with screen share + BFCP)
```

### BFCP State Machine
```
Phase 4: NOT STARTED (no BFCP exchange)
  ↓
Phase 5 (Expected):
  HELLO → HELLO_ACK → WAIT_FLOOR_REQUEST
  ↓
  FLOOR_REQUEST → PENDING → GRANTED
  ↓
  FLOOR_GRANTED → RTP ACTIVE
```

### Screen Share State
```
NOT_STARTED
  ↓ OnScreenShareTrack()
PIPELINE_CREATED (GStreamer running)
  ↓ sendScreenShareReInvite()
BLOCKED (waiting for SDP negotiation - 481 error)
  ↓ (Expected: 200 OK)
SDP_NEGOTIATED
  ↓ BFCP floor granted
RTP_ACTIVE (sending H264 packets)
```

---

## Key Timing Constraints

1. **GStreamer Pipeline**: Must be running BEFORE floor grant (currently OK ✓)
2. **RTP Destinations**: Must be set from SDP answer BEFORE sending RTP (blocked)
3. **BFCP Grant**: Must receive BEFORE sending RTP packets (not reached)
4. **TCP Connection**: re-INVITE MUST use same connection as dialog (FAILING ✗)

---

## Problem Summary: TCP Connection Reuse

### Root Cause
```go
// Current code: pkg/sip/inbound.go:1330
tx, err := c.cc.Transaction(req)
  ↓
// sipgo library creates NEW outbound TCP connection
// SIP device expects re-INVITE on ORIGINAL inbound connection
```

### Evidence

**Working (Inbound re-INVITE):**
```
SIP Device → LiveKit on Connection 1
ServerTransaction receives on correct connection
200 OK sent on same connection
✓ Video start/stop works
```

**Failing (Outbound re-INVITE):**
```
LiveKit → SIP Device on NEW Connection 2
ClientTransaction creates new connection
SIP device rejects (481 - wrong connection)
✗ Screen share blocked
```

### Solution Needed

Must send re-INVITE on the **same TCP connection** that received the original INVITE.

**Options:**
1. Use sipgo's lower-level transport APIs to send on existing connection
2. Hook into sipgo's connection pool to reuse dialog connection
3. Modify sipgo to support "Transaction on existing connection"
4. Use WriteRequest() but add custom response handling

---

## Next Steps to Unblock Phase 5

### Immediate Actions:
1. ✓ Document complete flow (this file)
2. Investigate sipgo connection reuse APIs
3. Test Transaction() with explicit connection parameter
4. Consider WriteRequest() + manual response correlation

### Testing After Fix:
1. Verify 200 OK received on re-INVITE
2. Check SDP answer includes screen share + BFCP
3. Confirm RTP destinations updated
4. Verify BFCP floor grant received
5. Confirm screen share video on SIP device

---

## Appendix: Code References

### Key Files
- [PHASE5_PLAN.md](PHASE5_PLAN.md) - Implementation plan
- [pkg/sip/inbound.go:1150-1462](pkg/sip/inbound.go) - re-INVITE implementation
- [pkg/sip/screenshare.go:414-477](pkg/sip/screenshare.go) - BFCP client logic
- [pkg/sip/server.go](pkg/sip/server.go) - BFCP server startup
- [github.com/vopenia/bfcp/server.go](../bfcp/server.go) - BFCP server implementation

### Log Files
- [debug.txt](debug.txt) - Complete session logs with 481 error

---

**Status:** Screen share flow blocked at Phase 5.5 (outbound re-INVITE) due to TCP connection reuse issue. All other infrastructure (GStreamer, BFCP server, SDP building) is ready and working.
