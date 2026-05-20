package livekitcompositor

import (
	"fmt"
	"hash/fnv"
	"math"
	"os"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/vopenia-io/go-pangocairo/cairo"
	"github.com/vopenia-io/go-pangocairo/pango"
)

type overlayCache struct {
	infos    []participantOverlayInfo
	vW       int
	vH       int
	nTracks  int
	muteIcon *cairo.Surface
}

type participantOverlayInfo struct {
	name       string
	muted      bool
	noCamera   bool
	audioLevel float64
}

func (e *LivekitCompositor) refreshOverlayCache() {
	e.mu.Lock()
	defer e.mu.Unlock()

	cache := &overlayCache{
		infos:    e.collectParticipantOverlayInfo(),
		vW:       int(e.videoWidth),
		vH:       int(e.videoHeight),
		nTracks:  len(e.currentLayout),
		muteIcon: e.LivekitCompositorCamera.muteIcon,
	}

	e.LivekitCompositorCamera.overlayCache.Store(cache)
}

// AvatarColor returns a deterministic #RRGGBB hex color for the given name.
// Saturation and lightness are fixed so the result always looks decent.
func AvatarColor(name string) string {
	h := fnv.New32a()
	h.Write([]byte(name))
	hue := float64(h.Sum32() % 360)

	r, g, b := hslToRGB(hue, 0.65, 0.50) // tweak S/L to taste
	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}

// hslToRGB: h in [0,360), s and l in [0,1]. Returns 0–255 RGB.
func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255)
}

// loadEmbeddedPNG decodes an embedded PNG into a cairo image surface by way
// of a temp file. The temp file is removed after cairo has finished decoding;
// the surface owns its own pixel buffer.
func loadEmbeddedPNG(data []byte) (*cairo.Surface, error) {
	f, err := os.CreateTemp("", "livekit-overlay-*.png")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return cairo.NewSurfaceFromPNG(f.Name())
}

func (e *LivekitCompositor) collectParticipantOverlayInfo() []participantOverlayInfo {
	out := make([]participantOverlayInfo, len(e.currentLayout))
	for i, sid := range e.currentLayout {
		p := e.participants[sid]
		name := p.Name
		if name == "" {
			name = sid
		}
		_, hasMic := e.tracks[livekit.TrackSource_MICROPHONE][sid]
		_, hasCam := e.tracks[livekit.TrackSource_CAMERA][sid]
		level := 0.0
		if hasMic {
			level = float64(p.Level)
		}
		// fmt.Printf("Collecting overlay info for participant %s: name=%s, hasMic=%v, hasCam=%v, level=%.2f\n", sid, name, hasMic, hasCam, level)
		out[i] = participantOverlayInfo{
			name:       name,
			muted:      !hasMic,
			noCamera:   !hasCam,
			audioLevel: level,
		}
	}
	return out
}

func (e *LivekitCompositor) cameraOverlayDrawCallback(self *gst.Bin, overlay *gst.Element, cr *cairo.Context, timestamp gst.ClockTime) {
	cache := e.LivekitCompositorCamera.overlayCache.Load()
	if cache == nil {
		return
	}
	infos := cache.infos
	vW := cache.vW
	vH := cache.vH
	nTracks := cache.nTracks
	muteIcon := cache.muteIcon

	if nTracks == 0 {
		return
	}

	videoW := float64(vW)
	videoH := float64(vH)

	// path_rounded_rect: build a rounded-rect path at (x,y,w,h) with corner
	// radius r, clamped so the four arcs don't overlap.
	pathRoundedRect := func(x, y, w, h, r float64) {
		if maxR := math.Min(w, h) / 2; r > maxR {
			r = maxR
		}
		cr.MoveTo(x+w-r, y)
		cr.Arc(x+w-r, y+r, r, -math.Pi/2, 0)
		cr.Arc(x+w-r, y+h-r, r, 0, math.Pi/2)
		cr.Arc(x+r, y+h-r, r, math.Pi/2, math.Pi)
		cr.Arc(x+r, y+r, r, math.Pi, 3*math.Pi/2)
		cr.ClosePath()
	}

	// pathParticipantRect: rounded rect that frames participant idx's tile.
	const cornerRadius = 24.0
	pathParticipantRect := func(idx int) {
		w, h, x, y := cameraComputeSize(vW, vH, idx, nTracks)
		pathRoundedRect(float64(x), float64(y), float64(w), float64(h), cornerRadius)
	}

	// 1. Mask everything outside the rounded tile interiors with black,
	// so the underlying compositor video is only visible inside the rounded
	// rects. One even-odd fill: outer frame rect XOR'd against N tile holes.
	func() {
		cr.Save()
		defer cr.Restore()
		cr.SetFillRule(cairo.FILL_RULE_EVEN_ODD)
		cr.SetSourceRGBA(0, 0, 0, 1)
		cr.Rectangle(0, 0, videoW, videoH)
		for idx := range infos {
			pathParticipantRect(idx)
		}
		cr.Fill()
	}()

	// 2. Per-tile decoration.
	drawAvatar := func(cx, cy, radius float64, name, initial string) {
		// Flat-filled disc, color derived from the participant name.
		var rByte, gByte, bByte uint8
		fmt.Sscanf(AvatarColor(name), "#%02X%02X%02X", &rByte, &gByte, &bByte)

		cr.Save()
		cr.SetSourceRGBA(float64(rByte)/255, float64(gByte)/255, float64(bByte)/255, 1.0)
		cr.Arc(cx, cy, radius, 0, 2*math.Pi)
		cr.Fill()
		cr.Restore()

		// Bold initial centered on the disc.
		layout := pango.CairoCreateLayout(cr)
		desc := pango.FontDescriptionFromString(fmt.Sprintf("Sans Bold %d", int(radius*1.1)))
		layout.SetFontDescription(desc)
		layout.SetText(initial, -1)
		pw, ph := layout.GetSize()
		w := float64(pw) / float64(pango.SCALE)
		h := float64(ph) / float64(pango.SCALE)

		cr.Save()
		cr.SetSourceRGBA(1, 1, 1, 1)
		cr.MoveTo(cx-w/2, cy-h/2)
		pango.CairoShowLayout(cr, layout)
		cr.Restore()
	}

	drawLabelPill := func(text string, x, y float64, fontPx int) {
		layout := pango.CairoCreateLayout(cr)
		desc := pango.FontDescriptionFromString(fmt.Sprintf("Sans %d", fontPx))
		layout.SetFontDescription(desc)
		layout.SetText(text, -1)
		pw, ph := layout.GetSize()
		w := float64(pw) / float64(pango.SCALE)
		h := float64(ph) / float64(pango.SCALE)

		const padX, padY = 10.0, 4.0
		cr.Save()
		cr.SetSourceRGBA(0, 0, 0, 0.55)
		pathRoundedRect(x, y, w+2*padX, h+2*padY, 6)
		cr.Fill()
		cr.SetSourceRGBA(1, 1, 1, 1)
		cr.MoveTo(x+padX, y+padY)
		pango.CairoShowLayout(cr, layout)
		cr.Restore()
	}

	drawMuteIcon := func(cx, cy float64) {
		if muteIcon == nil {
			return
		}
		iw := float64(muteIcon.GetWidth())
		ih := float64(muteIcon.GetHeight())
		const bgPad = 8.0
		cr.Save()
		cr.SetSourceRGBA(0.8, 0.8, 0.8, 0.25)
		pathRoundedRect(cx-iw/2-bgPad, cy-ih/2-bgPad, iw+2*bgPad, ih+2*bgPad, 8)
		cr.Fill()
		cr.SetSourceSurface(muteIcon, cx-iw/2, cy-ih/2)
		cr.Paint()
		cr.Restore()
	}

	for idx, info := range infos {
		w, h, tx, ty := cameraComputeSize(vW, vH, idx, nTracks)
		x := float64(tx)
		y := float64(ty)
		tw := float64(w)
		th := float64(h)

		// Tile outline (drawn AFTER mask so the stroke straddles the
		// boundary correctly — half over the black, half over the video).
		cr.Save()
		cr.SetSourceRGBA(0.26587, 0.54004, 0.94434, 0.9)
		outlineWidth := min(4, info.audioLevel*20)
		// fmt.Printf("Drawing outline for participant %s with audio level %.2f: width=%.2f\n", info.name, info.audioLevel, outlineWidth)
		cr.SetLineWidth(outlineWidth)
		pathRoundedRect(x, y, tw, th, cornerRadius)
		cr.Stroke()
		cr.Restore()

		// Camera-off: big avatar disc with bold initial.
		if info.noCamera {
			r := math.Min(tw, th) * 0.28
			initial := "?"
			if len(info.name) > 0 {
				initial = string([]rune(info.name)[0])
			}
			drawAvatar(x+tw/2, y+th/2-8, r, info.name, initial)
		}

		// Name pill, bottom-left of tile.
		drawLabelPill(info.name, x+16, y+th-40, 16)

		// Mute icon, top-right of tile (inset so it doesn't sit on the
		// rounded corner).
		if info.muted {
			drawMuteIcon(x+tw-40, y+th-40)
		}
	}

	if status := cr.Status(); status != cairo.STATUS_SUCCESS {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("cairo context in error state after camera overlay draw: %v", int(status)))
	}
}
