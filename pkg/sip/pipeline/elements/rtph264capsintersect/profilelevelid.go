package rtph264capsintersect

import (
	"encoding/hex"
	"fmt"
	"strings"
)

type profile int

const (
	profileUnknown profile = iota
	profileConstrainedBaseline
	profileBaseline
	profileMain
	profileHigh
	profileConstrainedHigh
)

const defaultProfileLevelID = "42e01f"

type parsedProfileLevelID struct {
	profile    profile
	profileIDC uint8
	profileIOP uint8
	levelIDC   uint8
	isLevel1b  bool
}

func parseProfileLevelID(s string) (parsedProfileLevelID, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 6 {
		return parsedProfileLevelID{}, fmt.Errorf("profile-level-id must be 6 hex chars, got %q", s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return parsedProfileLevelID{}, fmt.Errorf("invalid hex in profile-level-id %q: %w", s, err)
	}

	p := parsedProfileLevelID{
		profileIDC: b[0],
		profileIOP: b[1],
		levelIDC:   b[2],
	}
	p.profile = identifyProfile(p.profileIDC, p.profileIOP)
	p.isLevel1b = isLevel1b(p.profileIDC, p.profileIOP, p.levelIDC)
	return p, nil
}

// identifyProfile implements the RFC 6184 profile identification table.
// Match order matters: Constrained Baseline before Baseline, Constrained High before High.
func identifyProfile(idc, iop uint8) profile {
	switch idc {
	case 0x42:
		// csf1 (bit 6) set → Constrained Baseline; otherwise Baseline
		if iop&0x40 != 0 {
			return profileConstrainedBaseline
		}
		return profileBaseline

	case 0x4D:
		// csf0 (bit 7) set → Constrained Baseline; otherwise Main
		if iop&0x80 != 0 {
			return profileConstrainedBaseline
		}
		return profileMain

	case 0x58:
		// Both csf0+csf1 set → Constrained Baseline
		if iop&0xC0 == 0xC0 {
			return profileConstrainedBaseline
		}
		// csf0 set only → Baseline
		if iop&0x80 != 0 {
			return profileBaseline
		}
		return profileUnknown

	case 0x64:
		// Constrained High: csf4+csf5 (bits 3,2) set
		if iop&0x0C == 0x0C {
			return profileConstrainedHigh
		}
		// High: no constraint flags
		if iop == 0x00 {
			return profileHigh
		}
		return profileUnknown
	}

	return profileUnknown
}

// isLevel1b detects the Level 1b special case per RFC 6184.
func isLevel1b(profileIDC, profileIOP, levelIDC uint8) bool {
	switch profileIDC {
	case 0x42, 0x4D, 0x58:
		// For baseline-family: levelIDC==11 and csf3 (bit 4) set means 1b
		return levelIDC == 11 && profileIOP&0x10 != 0
	default:
		return levelIDC == 9
	}
}

// levelOrd returns an ordinal for level comparison.
// Ordering: 1(10) < 1b(11) < 1.1(11) < 1.2(12) < 1.3(13) < 2(20) < ...
// We map: level 10 → 10, 1b → 11, non-1b levelIDC 11 → 12, then 12→13, etc.
func levelOrd(p parsedProfileLevelID) int {
	if p.isLevel1b {
		return 11
	}
	v := int(p.levelIDC)
	if v <= 10 {
		return v
	}
	// levelIDC >= 11: shift up by 1 to make room for 1b at ordinal 11
	return v + 1
}

type canonicalForm struct {
	profileIDC uint8
	profileIOP uint8
}

var canonicalForms = map[profile]canonicalForm{
	profileConstrainedBaseline: {0x42, 0xe0},
	profileBaseline:            {0x42, 0x00},
	profileMain:                {0x4D, 0x00},
	profileHigh:                {0x64, 0x00},
	profileConstrainedHigh:     {0x64, 0x0C},
}

// compatibleProfile returns the common (most constrained) profile if two profiles
// are compatible, or false if they are not. Constrained Baseline is a subset of
// Baseline, and Constrained High is a subset of High.
func compatibleProfile(a, b profile) (profile, bool) {
	if a == b {
		return a, true
	}
	switch {
	case (a == profileConstrainedBaseline && b == profileBaseline) ||
		(a == profileBaseline && b == profileConstrainedBaseline):
		return profileConstrainedBaseline, true
	case (a == profileConstrainedHigh && b == profileHigh) ||
		(a == profileHigh && b == profileConstrainedHigh):
		return profileConstrainedHigh, true
	}
	return profileUnknown, false
}

// intersectProfileLevelID performs RFC 6184 semantic intersection of two profile-level-id values.
// Returns the result hex string and true if compatible, or ("", false) if profiles differ.
func intersectProfileLevelID(upstream, downstream string) (string, bool) {
	if upstream == "" {
		upstream = defaultProfileLevelID
	}

	u, err := parseProfileLevelID(upstream)
	if err != nil {
		return upstream, true // malformed → pass through
	}

	if downstream == "" {
		return upstream, true // no downstream constraint → pass through upstream
	}

	d, err := parseProfileLevelID(downstream)
	if err != nil {
		return upstream, true // malformed → pass through
	}

	if u.profile == profileUnknown || d.profile == profileUnknown {
		return upstream, true // unknown profile → fall through to string comparison (pass through)
	}

	// Determine the common profile. Constrained Baseline is a subset of Baseline,
	// and Constrained High is a subset of High, so these pairs are compatible —
	// the intersection is the constrained variant.
	_, ok := compatibleProfile(u.profile, d.profile)
	if !ok {
		return "", false
	}

	// Pick min level. If min is downstream's level, return the original downstream
	// string verbatim to preserve case for GStreamer's string intersection.
	if levelOrd(u) >= levelOrd(d) {
		return downstream, true
	}

	// Upstream has a lower level than downstream — must emit downstream's profile
	// encoding with upstream's (lower) level value.
	outIDC := d.profileIDC
	outIOP := d.profileIOP
	outLevel := u.levelIDC

	// Level 1b serialization exception
	if u.isLevel1b {
		switch outIDC {
		case 0x42, 0x4D, 0x58:
			outLevel = 0x0B
			outIOP |= 0x10 // set csf3
		default:
			outLevel = 0x09
		}
	}

	return fmt.Sprintf("%02x%02x%02x", outIDC, outIOP, outLevel), true
}
