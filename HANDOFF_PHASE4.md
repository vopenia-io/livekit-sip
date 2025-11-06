# 🚀 Session Handoff - Phase 4: SDP BFCP Parsing

**Copy/paste this entire document into your next Claude Code session to continue**

---

## 📍 Current Status

**Branch**: `wip-bfcp-screenshare`
**Last Commit**: `becdc7f` - "Phase 2+3: Room integration and inbound call lifecycle for screen share"
**Date**: 2025-11-06

### ✅ Completed Phases

- ✅ **Phase 1**: Enable screenshare code (commit `4210872`)
- ✅ **Phase 2**: Room integration (commit `becdc7f`)
- ✅ **Phase 3**: Inbound call integration (commit `becdc7f`)

**Test Status**: All tests pass. Camera video works both directions, screen share detection working, GStreamer pipeline activated.

### 📋 Next Task

Continue with **Phase 4: SDP BFCP Parsing** (2-3 hours)

---

## 📂 Key Files Reference

- **Main Analysis**: `SCREENSHARE_ANALYSIS.md` (comprehensive implementation guide)
- **Screenshare Code**: `pkg/sip/screenshare.go` (758 lines - complete)
- **Room**: `pkg/sip/room.go` (✅ Phase 2 complete)
- **Inbound Call**: `pkg/sip/inbound.go` (✅ Phase 3 complete, needs Phase 4 updates)
- **SDP Parser**: `pkg/media-sdk/sdp/v2/*.go` (needs Phase 4 extensions)

---

## 🎯 Your Next Task: Phase 4 - SDP BFCP Parsing

### Goal

Extract BFCP parameters from Poly's SDP offer and use real values instead of hardcoded placeholders.

### Current Problem

Phase 3 uses hardcoded values in `SetupScreenShare()` (inbound.go:1030):
```go
bfcpServerAddr := "192.168.0.104:5070" // Placeholder
// ...
ssm.SetupBFCP(ctx, bfcpServerAddr, 1, 1, 1) // conferenceID:1, userID:1, floorID:1
```

### What Poly Sends in SDP

Example BFCP section from Poly device:
```sdp
m=application 16886 TCP/BFCP *
c=IN IP4 192.168.0.104
a=floorctrl:c-s
a=confid:1
a=userid:2
a=floorid:1 mstrm:3
a=setup:actpass
a=connection:new
```

### What to Parse

| Attribute | Example Value | How to Extract | Usage |
|-----------|---------------|----------------|-------|
| **Server Address** | `192.168.0.104` | `c=` line | BFCP connection |
| **Server Port** | `16886` | `m=application` line | BFCP connection |
| **Conference ID** | `1` | `a=confid:1` | SetupBFCP parameter |
| **User ID** | `2` | `a=userid:2` | SetupBFCP parameter |
| **Floor ID** | `1` | `a=floorid:1 mstrm:3` | SetupBFCP parameter |
| **Floor Control** | `c-s` | `a=floorctrl:c-s` | Validation (client-server) |
| **Media Stream** | `3` | `mstrm:3` in floorid | Link floor to video m-line |

---

## 📝 Implementation Steps

### Step 1: Extend SDP Parser to Handle BFCP Media

**File**: Check if SDP parser in `pkg/media-sdk/sdp/v2/` or local fork

**What to Add**:
```go
// In SDP struct
type SDP struct {
    // ... existing fields ...
    BFCP *SDPMedia  // NEW - BFCP application media section
}

// In SDPMedia or new BFCPMedia struct
type BFCPMedia struct {
    Port         uint16
    ConnectionIP netip.Addr
    FloorCtrl    string  // "c-s", "c-only", "s-only"
    ConferenceID uint32
    UserID       uint16
    FloorID      uint16
    MediaStream  uint16  // mstrm value
    Setup        string  // "active", "passive", "actpass"
    Connection   string  // "new", "existing"
}
```

**Parsing Logic**:
1. Detect `m=application <port> TCP/BFCP *` or `TCP/TLS/BFCP`
2. Parse BFCP-specific attributes:
   - `a=floorctrl:` → `FloorCtrl`
   - `a=confid:` → `ConferenceID`
   - `a=userid:` → `UserID`
   - `a=floorid:<id> mstrm:<stream>` → `FloorID` and `MediaStream`
   - `a=setup:` → `Setup`
   - `a=connection:` → `Connection`
3. Extract connection address from `c=IN IP4 <addr>`

### Step 2: Update SetupScreenShare to Use Parsed Values

**File**: `pkg/sip/inbound.go` (around line 1025-1081)

**Replace**:
```go
// OLD - Hardcoded values
bfcpServerAddr := "192.168.0.104:5070"
// ...
ssm.SetupBFCP(ctx, bfcpServerAddr, 1, 1, 1)
```

**With**:
```go
// NEW - Extract from SDP
if offer.BFCP != nil {
    bfcpAddr := offer.BFCP.ConnectionIP
    bfcpPort := offer.BFCP.Port
    bfcpServerAddr := fmt.Sprintf("%s:%d", bfcpAddr, bfcpPort)

    conferenceID := offer.BFCP.ConferenceID
    userID := offer.BFCP.UserID
    floorID := offer.BFCP.FloorID

    c.log.Infow("🖥️ [Inbound] Parsed BFCP from SDP",
        "serverAddr", bfcpServerAddr,
        "conferenceID", conferenceID,
        "userID", userID,
        "floorID", floorID,
        "floorCtrl", offer.BFCP.FloorCtrl,
        "mstrm", offer.BFCP.MediaStream,
    )

    if err := ssm.SetupBFCP(ctx, bfcpServerAddr, conferenceID, userID, floorID); err != nil {
        c.log.Warnw("🖥️ [Inbound] Failed to setup BFCP client", err)
    }
} else {
    c.log.Infow("🖥️ [Inbound] No BFCP in SDP offer, using defaults")
    // Fallback to defaults for testing without BFCP
}
```

### Step 3: Add Validation

**Validations to Add**:
```go
// Validate floor control mode
if offer.BFCP.FloorCtrl != "c-s" && offer.BFCP.FloorCtrl != "c-only" {
    c.log.Warnw("Unexpected floor control mode", nil, "floorCtrl", offer.BFCP.FloorCtrl)
}

// Validate TCP setup role
if offer.BFCP.Setup != "active" && offer.BFCP.Setup != "passive" && offer.BFCP.Setup != "actpass" {
    c.log.Warnw("Unexpected TCP setup role", nil, "setup", offer.BFCP.Setup)
}

// Log media stream mapping
c.log.Infow("BFCP floor mapped to video stream", "floorID", floorID, "mstrm", offer.BFCP.MediaStream)
```

### Step 4: Test with Real Poly SDP

**Testing Checklist**:
1. ✅ Build successfully
2. ✅ Start call from Poly
3. ✅ Verify BFCP parameters extracted from SDP (check logs)
4. ✅ Verify BFCP connection succeeds (if Poly has BFCP enabled)
5. ✅ Start screen share from WebRTC
6. ✅ Verify floor request uses real parameters
7. ✅ Camera video still works

---

## 📖 Expected Logs After Phase 4

### During Call Setup
```
Parsed BFCP from SDP:
    serverAddr: 192.168.0.104:16886
    conferenceID: 1
    userID: 2
    floorID: 1
    floorCtrl: c-s
    mstrm: 3
🖥️ [ScreenShare] Setting up BFCP client
    serverAddr: 192.168.0.104:16886
    conferenceID: 1
    userID: 2
    floorID: 1
```

### When Screen Share Starts
```
🖥️ [ScreenShare] Step 1/4: Requesting BFCP floor control
🖥️ [ScreenShare] [RequestFloor] Requesting presentation floor via BFCP
    floorID: 1
    userID: 2
    beneficiaryID: 2
    serverAddr: 192.168.0.104:16886
```

### Success Case (if Poly responds)
```
🖥️ [BFCP] ✅ Connected to BFCP server
🖥️ [BFCP] ✅ Floor GRANTED
    floorID: 1
    requestID: 1
```

---

## 🔧 Implementation Tips

### Finding the SDP Parser

The SDP parser is in `bfcp/sdp.go`  local repo updatable




### Regex Patterns for Manual Parsing

If needed, use these patterns:
```go
// m=application <port> TCP/BFCP *
bfcpMediaLine := regexp.MustCompile(`m=application\s+(\d+)\s+TCP(?:/TLS)?/BFCP`)

// a=confid:<value>
confIDPattern := regexp.MustCompile(`a=confid:(\d+)`)

// a=userid:<value>
userIDPattern := regexp.MustCompile(`a=userid:(\d+)`)

// a=floorid:<id> mstrm:<stream>
floorIDPattern := regexp.MustCompile(`a=floorid:(\d+)(?:\s+mstrm:(\d+))?`)

// a=floorctrl:<mode>
floorCtrlPattern := regexp.MustCompile(`a=floorctrl:([\w-]+)`)
```

---

## 🚨 Important Notes

### Don't Break Existing Functionality

- Keep fallback to defaults if no BFCP in SDP
- Don't fail the call if BFCP parsing fails
- Maintain backward compatibility

### Testing Without BFCP

If Poly doesn't send BFCP in initial INVITE:
- Parser should gracefully handle missing BFCP section
- Log warning: "No BFCP in SDP offer, screen share will use defaults"
- Screen share should still work (for testing pipeline)

### Phase 4 Scope

**In Scope**:
- ✅ Parse BFCP parameters from SDP
- ✅ Use real values in SetupBFCP()
- ✅ Validate and log parsed values

**Out of Scope** (Phase 5):
- ❌ Generate BFCP in our SDP answer
- ❌ Send re-INVITE with screen share
- ❌ Multiple video m-lines in SDP

---

## 📊 Progress Summary

| Phase | Status | Time Estimate | Description |
|-------|--------|---------------|-------------|
| Phase 1 | ✅ DONE | 5 min | Enable screenshare code |
| Phase 2 | ✅ DONE | 30-45 min | Room integration |
| Phase 3 | ✅ DONE | 1-2 hours | Inbound call integration |
| Phase 4 | ⏳ NEXT | 2-3 hours | SDP BFCP parsing |
| Phase 5 | ⏳ TODO | 3-4 hours | SIP re-INVITE |
| Phase 6 | ⏳ TODO | 2-4 hours | End-to-end testing |

**Remaining: 7-9 hours**

---

## 🔗 Quick Reference

- **Analysis Doc**: `/home/vopenia/dev/livekit-sip/SCREENSHARE_ANALYSIS.md`
- **Screenshare Code**: `/home/vopenia/dev/livekit-sip/pkg/sip/screenshare.go`
- **Inbound Code**: `/home/vopenia/dev/livekit-sip/pkg/sip/inbound.go`
- **Current Branch**: `wip-bfcp-screenshare`
- **Last Commit**: `becdc7f`

---

## 📝 Instructions for Next Session

**Copy/paste this to Claude Code:**

```
Continue screenshare implementation from Phase 4.

Current state:
- Branch: wip-bfcp-screenshare
- Last commit: becdc7f "Phase 2+3: Room integration and inbound call lifecycle for screen share"
- Phases 1-3: COMPLETE ✅ (tested, all passing)

Next task: Phase 4 - SDP BFCP Parsing
Goal: Extract BFCP parameters from Poly's SDP offer and use real values instead of hardcoded placeholders

Please read HANDOFF_PHASE4.md for full context, then implement Phase 4.

After implementation, I will:
1. Test with real Poly device
2. Share logs for analysis
3. Validate BFCP parameters extracted correctly
4. Commit with 1-liner message
5. Continue to Phase 5

Let's start with Phase 4!
```

---

## 🔧 Workflow Reminder

For EACH phase:
1. **I implement code**
2. **You test with Poly device**
3. **You share logs**
4. **I analyze logs**
5. **Together we validate**
6. **If pass → commit with 1-liner message**
7. **I update SCREENSHARE_ANALYSIS.md**
8. **Move to next phase**

---

**End of Handoff Document**
