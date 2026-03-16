//go:build debug_ui

package vp8h264select

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/debug"
)

//go:embed debug.html
var debugHTML []byte

func init() {
	debug.Register("vp8_h264_select", func(element *gst.Element) http.Handler {
		e, ok := gst.SubclassFromElement[*Vp8H264Select](element)
		if !ok {
			return nil
		}
		return newDebugHandler(e)
	})
}

type padInfo struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type switchRequest struct {
	Pad string `json:"pad"`
}

func newDebugHandler(e *Vp8H264Select) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(debugHTML)
	})

	mux.HandleFunc("GET /api/pads", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()

		activeName := ""
		if e.InputSelector != nil {
			activeVal, err := e.InputSelector.GetProperty("active-pad")
			if err == nil && activeVal != nil {
				if activePad, ok := activeVal.(*gst.Pad); ok && activePad != nil {
					activeName = activePad.GetName()
				}
			}
		}

		pads := make([]padInfo, 0, len(e.Branches))
		for name := range e.Branches {
			pads = append(pads, padInfo{
				Name:   name,
				Active: name == activeName,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pads)
	})

	// mux.HandleFunc("POST /api/switch", func(w http.ResponseWriter, r *http.Request) {
	// 	var req switchRequest
	// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
	// 		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
	// 		return
	// 	}

	// 	e.mu.Lock()
	// 	branch, ok := e.Branches[req.Pad]
	// 	e.mu.Unlock()

	// 	if !ok {
	// 		http.Error(w, "pad not found: "+req.Pad, http.StatusNotFound)
	// 		return
	// 	}

	// 	srcPad := branch.VideoConvert.GetStaticPad("src")
	// 	if srcPad == nil {
	// 		http.Error(w, "failed to get src pad from branch videoconvert", http.StatusInternalServerError)
	// 		return
	// 	}

	// 	done := make(chan struct{})
	// 	defer close(done)
	// 	glib.IdleAdd(func() {
	// 		defer func() { done <- struct{}{} }()
	// 		structure := gst.NewStructure(SelectEventName)
	// 		runtime.SetFinalizer(structure, nil) // give ownership to the event
	// 		if !srcPad.PushEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstream, structure)) {
	// 			http.Error(w, "failed to push switch event on pad "+req.Pad, http.StatusInternalServerError)
	// 			return
	// 		}
	// 	})
	// 	<-done

	// 	w.Header().Set("Content-Type", "application/json")
	// 	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	// })

	mux.HandleFunc("POST /api/force-switch", func(w http.ResponseWriter, r *http.Request) {
		var req switchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		e.mu.Lock()
		branch, ok := e.Branches[req.Pad]
		inputSelector := e.InputSelector
		e.mu.Unlock()

		if !ok {
			http.Error(w, "pad not found: "+req.Pad, http.StatusNotFound)
			return
		}

		if inputSelector == nil {
			http.Error(w, "input selector not available", http.StatusInternalServerError)
			return
		}

		selectorSink := branch.Queue.GetStaticPad("src").GetPeer()
		if selectorSink == nil {
			http.Error(w, "failed to get input-selector sink pad for branch "+req.Pad, http.StatusInternalServerError)
			return
		}

		done := make(chan struct{})
		defer close(done)
		glib.IdleAdd(func() {
			defer func() { done <- struct{}{} }()
			if err := inputSelector.SetProperty("active-pad", selectorSink); err != nil {
				http.Error(w, "failed to set active-pad: "+err.Error(), http.StatusInternalServerError)
				return
			}
		})
		<-done

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	return mux
}
