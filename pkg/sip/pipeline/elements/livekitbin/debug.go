//go:build debug_ui

package livekitbin

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/go-gst/go-gst/gst"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/debug"
)

//go:embed debug.html
var debugHTML []byte

func init() {
	debug.Register("livekitbin", func(element *gst.Element) http.Handler {
		lkbin, ok := gst.SubclassFromElement[*LivekitBin](element)
		if !ok {
			return nil
		}
		return newDebugHandler(lkbin)
	})
}

type trackInfo struct {
	SID      string `json:"sid"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	MimeType string `json:"mimeType"`
	Enabled  bool   `json:"enabled"`
	Muted    bool   `json:"muted"`
}

type participantInfo struct {
	Identity string      `json:"identity"`
	SID      string      `json:"sid"`
	Name     string      `json:"name"`
	Tracks   []trackInfo `json:"tracks"`
}

type subscribeRequest struct {
	ParticipantSID string `json:"participantSID"`
	TrackSID       string `json:"trackSID"`
	Enable         bool   `json:"enable"`
}

func newDebugHandler(lkbin *LivekitBin) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(debugHTML)
	})

	mux.HandleFunc("GET /api/participants", func(w http.ResponseWriter, r *http.Request) {
		participants := lkbin.room.GetRemoteParticipants()
		result := make([]participantInfo, 0, len(participants))

		for _, p := range participants {
			pi := participantInfo{
				Identity: string(p.Identity()),
				SID:      string(p.SID()),
				Name:     p.Name(),
			}
			for _, pub := range p.TrackPublications() {
				rp, ok := pub.(*lksdk.RemoteTrackPublication)
				if !ok {
					continue
				}
				pi.Tracks = append(pi.Tracks, trackInfo{
					SID:      pub.SID(),
					Name:     pub.Name(),
					Kind:     pub.Kind().String(),
					Source:   pub.Source().String(),
					MimeType: pub.MimeType(),
					Enabled:  rp.IsEnabled(),
					Muted:    pub.IsMuted(),
				})
			}
			result = append(result, pi)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("POST /api/subscribe", func(w http.ResponseWriter, r *http.Request) {
		var reqs []subscribeRequest
		if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		var errors []string
		done := make(chan struct{})
		defer close(done)
		for _, req := range reqs {
			part := lkbin.room.GetParticipantBySID(req.ParticipantSID)
			if part == nil {
				errors = append(errors, req.ParticipantSID+": participant not found")
				continue
			}
			for _, pub := range part.TrackPublications() {
				if pub.SID() != req.TrackSID {
					continue
				}
				rp, ok := pub.(*lksdk.RemoteTrackPublication)
				if !ok {
					errors = append(errors, req.TrackSID+": not a remote track publication")
					continue
				}
				rp.SetEnabled(req.Enable)
				break
			}
		}

		if len(errors) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"errors": errors})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	return mux
}
