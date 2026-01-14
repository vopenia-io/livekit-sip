package pipeline

/*
#include <malloc.h>
*/
import "C"
import (
	"runtime"
	"runtime/debug"
)

// Call this after p.SetState(gst.StateNull) and p.Unref()
func ForceMemoryRelease() {
	runtime.GC()
	debug.FreeOSMemory()
	C.malloc_trim(0)
}
