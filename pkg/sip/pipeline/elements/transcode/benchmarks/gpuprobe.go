package benchmarks

// CGo wrapper for the dlopen-based NVML GPU utilization probe.
// The C code loads libnvidia-ml.so.1 at runtime so there is no
// build-time dependency on NVIDIA headers or libraries.

/*
#cgo LDFLAGS: -ldl
#include "gpuprobe.h"
*/
import "C"

import (
	"fmt"
	"os"
)

type gpuRawSample struct {
	smUtil  uint32
	encUtil uint32
	decUtil uint32
}

func gpuprobeInit() bool {
	ok := C.gpuprobe_init() == 0
	if !ok {
		fmt.Fprintf(os.Stderr, "gstbench: NVML init failed (no GPU or libnvidia-ml.so.1 not loadable)\n")
	}
	return ok
}

func gpuprobeSample() (gpuRawSample, bool) {
	var cs C.GPUSample
	if C.gpuprobe_sample(&cs) != 0 {
		return gpuRawSample{}, false
	}
	return gpuRawSample{
		smUtil:  uint32(cs.sm_util),
		encUtil: uint32(cs.enc_util),
		decUtil: uint32(cs.dec_util),
	}, true
}

func gpuprobeShutdown() {
	C.gpuprobe_shutdown()
}
