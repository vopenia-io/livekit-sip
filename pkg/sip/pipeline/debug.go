package pipeline

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/go-gst/go-gst/gst"
)

var ErrPipielineNotRunning = fmt.Errorf("pipeline not running")

var runningTimeRegex = regexp.MustCompile(`running-time=\d+`)

func sanitizeDot(dot string) string {
	return runningTimeRegex.ReplaceAllString(dot, "running-time=XXX")
}

func (p *Pipeline) Monitor() {
	name := p.Pipeline().GetName()

	dotFile, err := os.Create(fmt.Sprintf("%s_pipeline_live.dot", name))
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
