package pipeline

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

func (p *Pipeline) DumpDot() {
	ticker := time.NewTicker(5000 * time.Millisecond)

	dump := false
	mu := sync.Mutex{}
	count := 0

	onDumpCH := func() {
		mu.Lock()
		defer mu.Unlock()
		dump = true
	}

	dumpPipeline := func() {
		mu.Lock()
		defer mu.Unlock()
		if dump {
			dump = false
			p.Log.Infow("Dumping pipeline state to dot file")
			count++
			done := make(chan struct{})
			glib.IdleAdd(func() {
				p.Pipeline().DebugBinToDotFileWithTs(gst.DebugGraphShowAll|gst.DebugGraphShowFullParams, fmt.Sprintf("%s_pipeline_%d.dot", p.Pipeline().GetName(), count))
				close(done)
			})
			<-done
			p.Log.Infow("Pipeline state dumped to dot file")
		}
	}

	for {
		select {
		case <-p.closed.Watch():
			p.Log.Infow("Pipeline closed, exiting DumpDot loop")
			return
		case now := <-p.dumpCH:
			onDumpCH()
			if now {
				dumpPipeline()
			}
		case <-ticker.C:
			dumpPipeline()
		}
	}
}
