package event

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
)

func NewEventLoop(ctx context.Context) *EventLoop {
	ctx, cancel := context.WithCancel(ctx)
	return &EventLoop{
		ctx:      ctx,
		cancel:   cancel,
		register: make(chan CallbackHandler, 10),
	}
}

type CallbackHandler struct {
	loop *EventLoop
	done reflect.Value
	cb   reflect.Value
	args reflect.Value
	ret  reflect.Value
	inT  int
	outT int
	mu   *sync.Mutex
}

type EventLoop struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   atomic.Bool
	register  chan CallbackHandler
	callbacks []CallbackHandler
}

func (loop *EventLoop) Stop() {
	loop.cancel()
	loop.wg.Wait()
	loop.callbacks = nil
	loop.wg = sync.WaitGroup{}
}

func (loop *EventLoop) Run() {
	if loop.running.Swap(true) {
		fmt.Println("EventLoop: already running")
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	loop.wg.Add(1)
	defer loop.wg.Done()
	for {
		fmt.Printf("EventLoop: waiting for events...\n")
		loop.mu.Lock()
		cases := make([]reflect.SelectCase, len(loop.callbacks)*2+2)
		cases[0] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(loop.ctx.Done())}
		cases[1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(loop.register)}
		for i, handler := range loop.callbacks {
			pos := i*2 + 2
			cases[pos] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: handler.args}
			cases[pos+1] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: handler.done}
		}

		chosen, recv, ok := reflect.Select(cases)
		fmt.Printf("EventLoop: selected case %d (ok=%v)\n", chosen, ok)
		if chosen == 0 {
			loop.mu.Unlock()
			return
		}
		if !ok {
			loop.mu.Unlock()
			continue
		}
		if chosen == 1 {
			handler := recv.Interface().(CallbackHandler)
			loop.callbacks = append(loop.callbacks, handler)
			loop.mu.Unlock()
			continue
		}
		chosen -= 2
		if chosen%2 == 0 {
			// Message channel
			cb := loop.callbacks[chosen/2]

			args := make([]reflect.Value, cb.inT)
			for i := 0; i < cb.inT; i++ {
				args[i] = recv.Field(i)
			}
			fmt.Printf("EventLoop: invoking callback with %d args: %v\n", cb.inT, args)
			result := cb.cb.Call(args)
			fmt.Printf("EventLoop: callback returned %d results: %v\n", cb.outT, result)
			retStructFields := make([]reflect.StructField, cb.outT)
			for i := 0; i < cb.outT; i++ {
				retStructFields[i] = reflect.StructField{
					Name: fmt.Sprintf("Ret%d", i),
					Type: result[i].Type(),
				}
			}
			retStructType := reflect.StructOf(retStructFields)
			retStruct := reflect.New(retStructType).Elem()
			for i := 0; i < cb.outT; i++ {
				retStruct.Field(i).Set(result[i])
			}
			fmt.Printf("EventLoop: sending return struct: %v\n", retStruct)
			cb.ret.Send(retStruct)
			fmt.Printf("EventLoop: return struct sent\n")
			// cb.mu.Unlock()

		} else {
			// Done channel
			idx := chosen / 2
			loop.callbacks = slices.Delete(loop.callbacks, idx, idx+1)
		}
		loop.mu.Unlock()
	}
}

func (loop *EventLoop) Register(cb CallbackHandler) {
	loop.register <- cb
}
