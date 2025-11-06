# Comprehensive Logging Additions for Screen Share re-INVITE

This document details all the logging added to trace the screen share re-INVITE flow.

## Files Modified

### 1. `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`

#### buildReInviteSDP() - Phase 5.2-5.4
**Lines: 1157-1235**

**Added Logging:**
- Entry log with includeScreenShare flag
- Error log if initial answer is missing
- Detailed state of initial answer (hasAudio, hasVideo, hasBFCP, addr)
- Detailed state of initial offer from SIP device (with BFCP params)
- Clone operation log
- Audio state from initial answer
- Video state from initial answer
- Phase 5.3 placeholder logs for screen share video m-line
- Phase 5.4 placeholder logs for BFCP m-line (with expected parameters)
- Success log after SDP built

**Key Logs to Watch:**
```
🖥️ [re-INVITE] [Phase5.2] Building SDP (includeScreenShare: true/false)
🖥️ [re-INVITE] [Phase5.2] Initial answer state
🖥️ [re-INVITE] [Phase5.2] Initial offer state (from SIP device)
🖥️ [re-INVITE] [Phase5.3] Adding screen share video m-line
🖥️ [re-INVITE] [Phase5.4] Adding BFCP m-line as s-only server
```

#### sendScreenShareReInvite() - Phase 5.5
**Lines: 1237-1378**

**Added Logging:**
- Entry log when function called
- SDP ready log with size and full SDP body
- Original dialog state (From/To/Call-ID comparison)
- Request-URI selection log
- From/To header creation logs
- **CRITICAL CONNECTION STATE LOGGING:**
  - `inviteSource`: Where original INVITE came from
  - `inviteDest`: Where original INVITE went to
  - `inviteOkSource`: Where 200 OK came from
  - `inviteOkDest`: Where 200 OK went to
  - `requestSource`: Where re-INVITE will come from
  - `requestDest`: Where re-INVITE will go to
- Transaction creation success
- **🔍 CRITICAL marker to watch for "Dialing" in sipgo logs (indicates new connection)**
- Full INVITE request logged at debug level

**Key Logs to Watch:**
```
🖥️ [re-INVITE] [Phase5.5] Creating INVITE request
🖥️ [re-INVITE] [Phase5.5] Connection state (CRITICAL - shows source/dest for all messages)
🖥️ [re-INVITE] [Phase5.5] Transaction created successfully
🖥️ [re-INVITE] [Phase5.5] 🔍 CRITICAL: Check if new TCP connection was created (watch for 'Dialing' in logs)
```

#### waitForReInviteResponse() - Phase 5.6
**Lines: 1380-1412**

**Added Logging:**
- Entry log when entering wait loop
- Timeout error with ⏰ emoji
- Transaction done/failed error with ❌ emoji
- Response received from channel (with status, reason, From, To, Call-ID)
- Final response received (200-699) with ✅ emoji
- Provisional response (1xx) with 📞 emoji

**Key Logs to Watch:**
```
🖥️ [re-INVITE] [Phase5.6] Entering response wait loop
🖥️ [re-INVITE] [Phase5.6] Response received from channel (status: XXX)
🖥️ [re-INVITE] [Phase5.6] ✅ Final response received (status: 200)
🖥️ [re-INVITE] [Phase5.6] ❌ Non-200 response (shows full response headers)
```

#### handleReInviteResponse() - Phase 5.7-5.10
**Lines: 1414-1507**

**Added Logging:**
- Entry log with status and addScreenShare flag
- Error log for non-200 with full response string
- Success log for 200 OK
- Raw SDP body at debug level
- Successful SDP parse with hasAudio/hasVideo/hasBFCP/addr
- **Detailed Audio info:** port, payloadType, numCodecs
- **Detailed Video info:** port, rtcpPort, payloadType, numCodecs
- **Detailed BFCP info:** port, connectionIP, floorCtrl, conferenceID, userID, floorID
- Phase 5.8 connection update (if addScreenShare)
- Phase 5.9 ACK sending
- Phase 5.10 BFCP client setup with all parameters
- Success/failure for BFCP setup

**Key Logs to Watch:**
```
🖥️ [re-INVITE] [Phase5.7] ✅ 200 OK received, parsing SDP answer
🖥️ [re-INVITE] [Phase5.7] ✅ SDP parsed successfully
🖥️ [re-INVITE] [Phase5.7] Audio in SDP (port: XXXX, payloadType: YY)
🖥️ [re-INVITE] [Phase5.7] Video in SDP (port: XXXX, rtcpPort: YYYY)
🖥️ [re-INVITE] [Phase5.7] BFCP in SDP (all parameters)
🖥️ [re-INVITE] [Phase5.8] Updating screen share connections
🖥️ [re-INVITE] [Phase5.9] Sending ACK
🖥️ [re-INVITE] [Phase5.10] Setting up BFCP client connection
```

#### updateScreenShareConnections() - Phase 5.8
**Lines: 1509-1527**

**Added Logging:**
- Entry log with remote addr, rtpPort, rtcpPort
- Success log with rtpDst and rtcpDst addresses

**Key Logs to Watch:**
```
🖥️ [re-INVITE] [Phase5.8] Updating screen share connections
🖥️ [re-INVITE] [Phase5.8] ✅ Screen share connections updated (rtpDst: X, rtcpDst: Y)
```

## Log Flow for Successful Screen Share

### Expected Log Sequence (When Working):

```
1. Screen Share Published (WebRTC)
   🖥️ [ScreenShare] Screen share track detected
   🖥️ [ScreenShare] GStreamer pipeline created successfully
   🖥️ [Inbound] Screen share started - re-INVITE needed

2. Build SDP (Phase 5.2-5.4)
   🖥️ [re-INVITE] [Phase5.2] Building SDP (includeScreenShare: true)
   🖥️ [re-INVITE] [Phase5.2] Initial answer state (hasAudio: true, hasVideo: true)
   🖥️ [re-INVITE] [Phase5.2] Initial offer state (hasBFCP: true, conferenceID: 1)
   🖥️ [re-INVITE] [Phase5.2] Cloning initial answer for modification
   🖥️ [re-INVITE] [Phase5.3] Adding screen share video m-line
   🖥️ [re-INVITE] [Phase5.4] Adding BFCP m-line as s-only server
   🖥️ [re-INVITE] [Phase5.2] SDP built successfully

3. Send re-INVITE (Phase 5.5)
   🖥️ [re-INVITE] [Phase5.5] Creating INVITE request
   🖥️ [re-INVITE] [Phase5.5] Connection state (all source/dest addresses)
   🖥️ [re-INVITE] [Phase5.5] Creating transaction for re-INVITE
   ⚠️  WATCH FOR: "Dialing" in sipgo logs (= NEW CONNECTION = PROBLEM)
   🖥️ [re-INVITE] [Phase5.5] Transaction created successfully
   🖥️ [re-INVITE] [Phase5.5] Waiting for response...

4. Wait for Response (Phase 5.6)
   🖥️ [re-INVITE] [Phase5.6] Entering response wait loop
   🖥️ [re-INVITE] [Phase5.6] Response received from channel (status: 200)
   🖥️ [re-INVITE] [Phase5.6] ✅ Final response received

5. Handle 200 OK (Phase 5.7)
   🖥️ [re-INVITE] [Phase5.7] ✅ 200 OK received, parsing SDP answer
   🖥️ [re-INVITE] [Phase5.7] ✅ SDP parsed successfully
   🖥️ [re-INVITE] [Phase5.7] Audio in SDP
   🖥️ [re-INVITE] [Phase5.7] Video in SDP
   🖥️ [re-INVITE] [Phase5.7] BFCP in SDP

6. Update Connections (Phase 5.8)
   🖥️ [re-INVITE] [Phase5.8] Updating screen share connections
   🖥️ [re-INVITE] [Phase5.8] ✅ Screen share connections updated

7. Send ACK (Phase 5.9)
   🖥️ [re-INVITE] [Phase5.9] Sending ACK
   🖥️ [re-INVITE] [Phase5.9] ✅ ACK sent - transaction complete

8. Setup BFCP (Phase 5.10)
   🖥️ [re-INVITE] [Phase5.10] Setting up BFCP client connection
   🖥️ [BFCP] ✅ Connected to BFCP server

9. Request Floor (Phase 5.11)
   🖥️ [BFCP] Floor request sent
   🖥️ [BFCP] ✅ Floor GRANTED

10. RTP Flows (Phase 5.12)
    🖥️ [ScreenShare] ✅ Screen share RTP flowing
```

## Current Problem - Error Log Sequence:

```
1-2. (Same as above - Build SDP works)

3. Send re-INVITE (Phase 5.5)
   🖥️ [re-INVITE] [Phase5.5] Connection state
      inviteSource: 192.168.0.104:XXXX (SIP device)
      inviteDest: 192.168.0.10:5060 (LiveKit)
      requestSource: 192.168.0.10:5060 (LiveKit - correct)
      requestDest: 192.168.0.104:5060 (SIP device - correct)

   ⚠️  PROBLEM: sipgo creates NEW TCP connection
   🖥️ [re-INVITE] [Phase5.5] Transaction created successfully

4. Response is 481 (Phase 5.6)
   🖥️ [re-INVITE] [Phase5.6] Response received from channel (status: 481)
   🖥️ [re-INVITE] [Phase5.6] ❌ Non-200 response
      reason: "Call/Transaction Does Not Exist"

5. Error Handling
   🖥️ [re-INVITE] ❌ Request failed (status: 481)
   🖥️ [ScreenShare] ❌ re-INVITE callback failed
```

## Key Diagnostic Points

### 1. Connection State (Phase 5.5)
Look at the connection state log to see if source/dest are correct:
- `inviteSource` should be the SIP device IP (192.168.0.104)
- `inviteDest` should be LiveKit IP (192.168.0.10)
- `requestSource` should be LiveKit IP (swapped from inviteDest)
- `requestDest` should be SIP device IP (swapped from inviteSource)

### 2. New Connection Detection
**CRITICAL:** Watch sipgo logs for "Dialing" messages after "Transaction created":
- If you see "Dialing tcp://192.168.0.104:5060" → NEW CONNECTION (PROBLEM)
- Should reuse existing inbound TCP connection from original INVITE

### 3. Response Status (Phase 5.6)
- **200 OK** → Success, proceed to Phase 5.7
- **481 Call/Transaction Does Not Exist** → Dialog matching failed (usually connection issue)
- **488 Not Acceptable Here** → SDP format problem
- **500 Server Internal Error** → SIP device crashed or bug

### 4. SDP Parsing (Phase 5.7)
If we get 200 OK, check:
- `hasAudio: true` (should always be present)
- `hasVideo: true` (should be present for screen share)
- `hasBFCP: true` (should be present if we sent BFCP in offer)

### 5. BFCP Setup (Phase 5.10)
If SDP has BFCP, watch for:
- `🖥️ [BFCP] ✅ Connected to BFCP server`
- `🖥️ [BFCP] ✅ Floor GRANTED`

## Testing Instructions

### Test 1: Trigger Screen Share
1. Start call with video
2. Publish screen share from WebRTC client
3. Watch logs for full sequence above

### Test 2: Analyze Connection Issue
1. Grep logs for connection state:
   ```bash
   grep "Connection state" debug.txt
   ```
2. Check if sipgo creates new connection:
   ```bash
   grep -A5 "Transaction created" debug.txt | grep -i "dial"
   ```

### Test 3: Check Response
1. Find response status:
   ```bash
   grep "Final response received" debug.txt
   ```
2. If 481, check full response:
   ```bash
   grep -A10 "Non-200 response" debug.txt
   ```

## Files

- **Main Implementation**: `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`
- **Screen Share Manager**: `/home/vopenia/dev/livekit-sip/pkg/sip/screenshare.go`
- **BFCP Client**: `/home/vopenia/dev/bfcp/client.go`
- **BFCP Server**: `/home/vopenia/dev/bfcp/server.go`
- **Test Logs**: `/home/vopenia/dev/livekit-sip/debug.txt`
- **Flow Analysis**: `/home/vopenia/dev/livekit-sip/SCREENSHARE_FLOW_ANALYSIS.md`

## Next Steps

1. ✅ Logging added everywhere
2. Test with screen share
3. Analyze connection state logs
4. Identify root cause of TCP connection issue
5. Fix TCP connection reuse
6. Verify screen share RTP flows
