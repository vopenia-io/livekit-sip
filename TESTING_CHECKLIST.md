# Screen Share Testing Checklist

## What We Added

### 1. Comprehensive re-INVITE Logging
- **Phase 5.2-5.4**: SDP building with initial state logging
- **Phase 5.5**: TCP connection state tracking (critical for diagnosing 481 error)
- **Phase 5.6**: Response monitoring with full headers
- **Phase 5.7**: Detailed SDP parsing (audio/video/BFCP parameters)
- **Phase 5.8**: RTP/RTCP destination updates
- **Phase 5.9**: ACK sending
- **Phase 5.10**: BFCP client setup

### 2. RTP Packet Flow Monitoring
- **WebRTC → GStreamer**: Logs packets received from WebRTC client
- **GStreamer → SIP**: Logs H264 packets sent to SIP device
- **Periodic Statistics**: Every 5 seconds shows:
  - Total bytes transferred
  - Packet count
  - Average packet size

### 3. Analysis Documents
- **[SCREENSHARE_FLOW_ANALYSIS.md](SCREENSHARE_FLOW_ANALYSIS.md)**: Complete timing and state flow
- **[LOGGING_ADDITIONS.md](LOGGING_ADDITIONS.md)**: Detailed logging documentation

---

## Testing Procedure

### Step 1: Start Call
1. Start livekit-sip gateway
2. Make call from SIP device to LiveKit room
3. Verify audio and camera video work

**Expected Logs:**
```
INFO  SIP call established
INFO  Audio flowing
INFO  Video flowing
```

### Step 2: Publish Screen Share
1. From WebRTC client, click "Share Screen"
2. Select window/screen to share

**Expected Logs (WebRTC → GStreamer):**
```
INFO  🖥️ [ScreenShare] Screen share track detected
INFO  🖥️ [ScreenShare] GStreamer pipeline created successfully
INFO  🖥️ [ScreenShare] Starting WebRTC→GStreamer RTP copy goroutine
INFO  🖥️ [ScreenShare] RTP copy started (direction: WebRTC→GStreamer)
INFO  🖥️ [ScreenShare] Starting GStreamer→SIP RTP copy goroutine
INFO  🖥️ [ScreenShare] RTP copy started (direction: GStreamer→SIP)
INFO  🖥️ [ScreenShare] ✅ GStreamer pipeline started and PLAYING
INFO  🖥️ [ScreenShare] Marked as active
```

### Step 3: Monitor re-INVITE
Watch for re-INVITE being triggered:

**Expected Logs (re-INVITE Trigger):**
```
INFO  🖥️ [ScreenShare] Triggering SIP re-INVITE to add screen share m-line
INFO  🖥️ [Inbound] Screen share started - re-INVITE needed
INFO  🖥️ [Inbound] sendScreenShareReInvite called (addScreenShare: true)
```

### Step 4: Check SDP Building
**Expected Logs (Phase 5.2-5.4):**
```
INFO  🖥️ [re-INVITE] [Phase5.2] Building SDP (includeScreenShare: true)
INFO  🖥️ [re-INVITE] [Phase5.2] Initial answer state
      hasAudio: true
      hasVideo: true
      hasBFCP: false
      addr: 192.168.0.10
INFO  🖥️ [re-INVITE] [Phase5.2] Initial offer state (from SIP device)
      hasAudio: true
      hasVideo: true
      hasBFCP: true
      bfcpConferenceID: 1
INFO  🖥️ [re-INVITE] [Phase5.3] Adding screen share video m-line
INFO  🖥️ [re-INVITE] [Phase5.4] Adding BFCP m-line as s-only server
INFO  🖥️ [re-INVITE] [Phase5.2] SDP built successfully
```

### Step 5: Check Connection State (CRITICAL!)
**Expected Logs (Phase 5.5):**
```
INFO  🖥️ [re-INVITE] [Phase5.5] Connection state
      inviteSource: 192.168.0.104:XXXXX (SIP device)
      inviteDest: 192.168.0.10:5060 (LiveKit)
      inviteOkSource: 192.168.0.10:5060 (LiveKit)
      inviteOkDest: 192.168.0.104:XXXXX (SIP device)
      requestSource: 192.168.0.10:5060 (LiveKit)
      requestDest: 192.168.0.104:5060 (SIP device)
```

**⚠️ CRITICAL CHECK:**
```
INFO  🖥️ [re-INVITE] [Phase5.5] 🔍 CRITICAL: Check if new TCP connection was created
```
**After this line, watch for:**
- ✅ **Good**: No "Dialing" message → Reusing existing connection
- ❌ **Bad**: "Dialing tcp://192.168.0.104:5060" → NEW connection (will cause 481)

### Step 6: Check Response
**Success Case (200 OK):**
```
INFO  🖥️ [re-INVITE] [Phase5.6] Response received from channel (status: 200)
INFO  🖥️ [re-INVITE] [Phase5.6] ✅ Final response received (status: 200)
INFO  🖥️ [re-INVITE] [Phase5.7] ✅ 200 OK received, parsing SDP answer
INFO  🖥️ [re-INVITE] [Phase5.7] ✅ SDP parsed successfully
INFO  🖥️ [re-INVITE] [Phase5.7] Audio in SDP (port: XXXX)
INFO  🖥️ [re-INVITE] [Phase5.7] Video in SDP (port: YYYY)
INFO  🖥️ [re-INVITE] [Phase5.7] BFCP in SDP (port: 5070, connectionIP: 192.168.0.10)
```

**Failure Case (481):**
```
INFO  🖥️ [re-INVITE] [Phase5.6] Response received from channel (status: 481)
ERROR 🖥️ [re-INVITE] ❌ Non-200 response
      status: 481
      reason: "Call/Transaction Does Not Exist"
      responseHeaders: [full SIP response]
ERROR 🖥️ [re-INVITE] ❌ Request failed (status: 481)
ERROR 🖥️ [ScreenShare] ❌ re-INVITE callback failed
```

### Step 7: Check Connection Updates (If 200 OK)
**Expected Logs (Phase 5.8):**
```
INFO  🖥️ [re-INVITE] [Phase5.8] Updating screen share connections
      remote: 192.168.0.104
      rtpPort: XXXX
      rtcpPort: YYYY
INFO  setting media destination (addr: 192.168.0.104:XXXX)  [from udpConn]
INFO  🖥️ [re-INVITE] [Phase5.8] ✅ Screen share connections updated
      rtpDst: 192.168.0.104:XXXX
      rtcpDst: 192.168.0.104:YYYY
```

### Step 8: Check ACK (If 200 OK)
**Expected Logs (Phase 5.9):**
```
INFO  🖥️ [re-INVITE] [Phase5.9] Sending ACK
INFO  🖥️ [re-INVITE] [Phase5.9] ✅ ACK sent - transaction complete
```

### Step 9: Check BFCP Setup (If SDP has BFCP)
**Expected Logs (Phase 5.10):**
```
INFO  🖥️ [re-INVITE] [Phase5.10] Setting up BFCP client connection
INFO  🖥️ [re-INVITE] [Phase5.10] BFCP server address
      addr: 192.168.0.10:5070
      conferenceID: 1
      userID: 2
      floorID: 2
INFO  🖥️ [ScreenShare] Setting up BFCP client
INFO  [BFCP Client] Connecting to BFCP server (192.168.0.10:5070)
INFO  [BFCP Client] ✅ Connected to BFCP server
INFO  🖥️ [BFCP] ✅ Connected to BFCP server
INFO  🖥️ [re-INVITE] [Phase5.10] ✅ BFCP client connected
```

### Step 10: Check Floor Grant (If BFCP Connected)
**Expected Logs (Phase 5.11):**
```
INFO  [BFCP Client] Sending Hello message
INFO  [BFCP Client] Received HelloAck
INFO  [BFCP Client] Requesting floor (floorID: 2)
INFO  [BFCP Server] Received FloorRequest (floorID: 2, userID: 2)
INFO  [BFCP Server] Floor 2 granted to user 2
INFO  [BFCP Client] Received FloorRequestStatus (status: Granted)
INFO  🖥️ [BFCP] ✅ Floor GRANTED (floorID: 2, requestID: X)
```

### Step 11: Verify RTP Flow (Phase 5.12)
**Expected Logs (Every 5 seconds):**
```
INFO  🖥️ [ScreenShare] 📊 RTP flowing
      direction: WebRTC→GStreamer
      totalBytes: XXXXXXX
      packets: YYYY
      avgPacketSize: ZZZ

INFO  🖥️ [ScreenShare] 📊 RTP flowing
      direction: GStreamer→SIP
      totalBytes: XXXXXXX
      packets: YYYY
      avgPacketSize: ZZZ
```

### Step 12: Verify Video on SIP Device
1. Look at SIP device screen
2. Should see screen share video appear

---

## Diagnosis Guide

### Problem: 481 "Call/Transaction Does Not Exist"

**Root Cause:** Transaction() creates new TCP connection instead of reusing dialog connection

**How to Confirm:**
1. Check Step 5 logs - look for connection state
2. Look for "Dialing" message after transaction created
3. Compare `inviteSource` vs `requestDest` - should be same host/port

**Logs to Grep:**
```bash
# Check connection state
grep "Connection state" debug.txt

# Check for new connection
grep -A5 "Transaction created" debug.txt | grep -i "dial"

# Check response
grep "Response received" debug.txt
```

### Problem: No RTP Packets Flowing

**Possible Causes:**
1. UDP destination not set (check Step 7 logs)
2. BFCP floor not granted (check Step 10 logs)
3. GStreamer pipeline not running (check Step 2 logs)
4. WebRTC track not connected (check Step 2 logs)

**Logs to Check:**
```bash
# Check RTP statistics
grep "RTP flowing" debug.txt

# Check if destination was set
grep "setting media destination" debug.txt

# Check BFCP floor
grep "Floor GRANTED" debug.txt

# Check pipeline state
grep "pipeline started and PLAYING" debug.txt
```

### Problem: BFCP Connection Fails

**Possible Causes:**
1. SIP device didn't include BFCP in SDP answer
2. BFCP server not listening
3. Wrong BFCP parameters

**Logs to Check:**
```bash
# Check if SDP has BFCP
grep "BFCP in SDP" debug.txt

# Check BFCP server startup
grep "BFCP server listening" debug.txt

# Check BFCP client connection
grep "BFCP.*Connect" debug.txt
```

---

## Success Criteria

### ✅ Fully Working Screen Share:
1. WebRTC publishes screen share track
2. GStreamer pipeline receives VP8 packets
3. GStreamer transcodes VP8 → H264
4. re-INVITE sent with screen share + BFCP
5. SIP device responds 200 OK
6. ACK sent
7. BFCP client connects
8. BFCP floor granted
9. H264 RTP packets flow to SIP device
10. Screen share video appears on SIP device

### Current Status (Before Testing):
- ✅ Steps 1-2: GStreamer pipeline works
- ✅ Step 3: re-INVITE triggered
- ✅ Step 4: SDP built correctly
- ❌ Step 5: **BLOCKED - 481 error (TCP connection issue)**
- ⏸️ Steps 6-10: Not reached yet

---

## Next Steps

1. **Run test** with screen share
2. **Capture logs** to debug.txt
3. **Analyze** connection state logs from Step 5
4. **Identify** if new TCP connection is created
5. **Fix** TCP connection reuse issue
6. **Verify** screen share works end-to-end

## Log Capture Commands

```bash
# Full log capture
./livekit-sip > debug.txt 2>&1

# Real-time monitoring (in separate terminal)
tail -f debug.txt | grep "ScreenShare\|re-INVITE\|BFCP"

# After test - extract key sections
grep -A10 -B5 "Connection state" debug.txt > connection_analysis.txt
grep "RTP flowing" debug.txt > rtp_stats.txt
grep "BFCP" debug.txt > bfcp_flow.txt
```
