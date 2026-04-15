package h264rtppaybin

import (
	"strings"
	"testing"
)

func TestParseProfileLevelID(t *testing.T) {
	tests := []struct {
		plid       string
		wantProf   profile
		wantLevel  uint8
		want1b     bool
		wantErr    bool
	}{
		// Constrained Baseline 3.1 — common WebRTC baseline
		{"42e01f", profileConstrainedBaseline, 31, false, false},
		// Baseline (no constraint) 3.1
		{"42001f", profileBaseline, 31, false, false},
		// Main 3.1
		{"4d001f", profileMain, 31, false, false},
		// 640c1f is constrained-high per RFC 6184 (csf4+csf5 set); it
		// still encodes as profile_idc=0x64 so it maps to "high" for the
		// encoder's capsfilter (see gstH264ProfileName).
		{"640c1f", profileConstrainedHigh, 31, false, false},
		// High 4.2
		{"64002a", profileHigh, 42, false, false},
		// Level 1b (baseline family)
		{"42d00b", profileConstrainedBaseline, 11, true, false},
		// Malformed length
		{"640c", 0, 0, false, true},
		// Non-hex
		{"zzzzzz", 0, 0, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.plid, func(t *testing.T) {
			got, err := parseProfileLevelID(tc.plid)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (parsed=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.profile != tc.wantProf {
				t.Errorf("profile: got %d, want %d", got.profile, tc.wantProf)
			}
			if got.levelIDC != tc.wantLevel {
				t.Errorf("levelIDC: got %d, want %d", got.levelIDC, tc.wantLevel)
			}
			if got.isLevel1b != tc.want1b {
				t.Errorf("isLevel1b: got %v, want %v", got.isLevel1b, tc.want1b)
			}
		})
	}
}

func TestH264CapsStringForPLID(t *testing.T) {
	tests := []struct {
		plid        string
		wantProfile string
		wantLevel   string
	}{
		{"42e01f", "constrained-baseline", "3.1"},
		{"42001f", "baseline", "3.1"},
		{"4d001f", "main", "3.1"},
		{"640c1f", "high", "3.1"},
		{"64002a", "high", "4.2"},
		{"640c28", "high", "4"},
	}

	for _, tc := range tests {
		t.Run(tc.plid, func(t *testing.T) {
			s := h264CapsStringForPLID(tc.plid)
			if s == "" {
				t.Fatalf("got empty caps string for plid=%s", tc.plid)
			}
			if !strings.Contains(s, "profile=(string)"+tc.wantProfile) {
				t.Errorf("expected profile=%q in %q", tc.wantProfile, s)
			}
			if !strings.Contains(s, "level=(string)"+tc.wantLevel) {
				t.Errorf("expected level=%q in %q", tc.wantLevel, s)
			}
			if !strings.Contains(s, "stream-format=(string)avc") {
				t.Errorf("expected stream-format=avc in %q", s)
			}
			if !strings.Contains(s, "alignment=(string)au") {
				t.Errorf("expected alignment=au in %q", s)
			}
		})
	}
}

func TestH264CapsStringForPLID_Invalid(t *testing.T) {
	for _, plid := range []string{"", "xx", "000000", "ff00ff"} {
		if got := h264CapsStringForPLID(plid); got != "" {
			t.Errorf("plid=%q: expected empty, got %q", plid, got)
		}
	}
}

func TestMaxResolutionForLevel(t *testing.T) {
	tests := []struct {
		plid      string
		fps       int
		wantMinW  int // minimum expected width (at 16:9)
		wantMinH  int
	}{
		// Level 3.1: maxFS=3600 MBs. 16:9 -> 960x544 region. 16 * MB
		{"640c1f", 30, 900, 500},
		// Level 4.2: much higher, 1920x1088+ at 30fps
		{"64002a", 30, 1900, 1080},
		// Level 1.3: 396 MBs @ 16:9 -> ~448x224
		{"42c00d", 30, 400, 200},
	}
	for _, tc := range tests {
		t.Run(tc.plid, func(t *testing.T) {
			w, h, ok := maxResolutionForLevel(tc.plid, tc.fps)
			if !ok {
				t.Fatalf("plid=%s: not ok", tc.plid)
			}
			if w < tc.wantMinW || h < tc.wantMinH {
				t.Errorf("plid=%s: got %dx%d, want at least %dx%d", tc.plid, w, h, tc.wantMinW, tc.wantMinH)
			}
		})
	}
}

func TestMaxResolutionForLevel_Invalid(t *testing.T) {
	if _, _, ok := maxResolutionForLevel("not-hex", 30); ok {
		t.Errorf("expected !ok for malformed plid")
	}
}
