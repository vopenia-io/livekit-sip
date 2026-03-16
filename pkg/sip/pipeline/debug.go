package pipeline

import (
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var ErrPipelineNotRunning = fmt.Errorf("pipeline not running")

var runningTimeRegex = regexp.MustCompile(`running-time=\d+`)

func sanitizeDot(dot string) string {
	return runningTimeRegex.ReplaceAllString(dot, "running-time=XXX")
}

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
				p.Pipeline().DebugBinToDotFileWithTs(gst.DebugGraphShowAll, fmt.Sprintf("%s_pipeline_%d.dot", p.Pipeline().GetName(), count))
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

func (p *Pipeline) Monitor() {
	name := p.Pipeline().GetName()

	dotFile, err := os.Create(fmt.Sprintf("%s_pipeline_live", name))
	if err != nil {
		fmt.Printf("failed to create pipeline live log file: %v\n", err)
		return
	}

	go func() {
		defer dotFile.Close()
		defer func() {
			fmt.Printf("Pipeline %s monitor exiting\n", name)
		}()

		prevDot := ""

		for !p.closed.IsBroken() {
			dotData := p.Pipeline().DebugBinToDotData(gst.DebugGraphShowVerbose)
			dotData = sanitizeDot(dotData)

			if dotData != prevDot {
				fmt.Printf("Pipeline %s changed, updating dot file\n", name)
				prevDot = dotData
				dotFile.Truncate(0)
				dotFile.Seek(0, 0)
				dotFile.WriteString(dotData)
				dotFile.Sync()

				time.Sleep(100 * time.Millisecond)
			}

			time.Sleep(5000 * time.Millisecond)
		}
	}()
}
