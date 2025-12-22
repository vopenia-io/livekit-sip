package event

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/livekit/protocol/logger"
)

func MakeCallback[Callback any]() (Callback, *sync.Mutex, reflect.Value, reflect.Value) {
	typ := reflect.TypeFor[Callback]()

	logger.Debugw("making callback", "type", typ)

	if typ.Kind() != reflect.Func {
		return *new(Callback), &sync.Mutex{}, reflect.Value{}, reflect.Value{}
	}

	inT := typ.NumIn()
	outT := typ.NumOut()

	logger.Debugw("making callback inputs", "count", inT)
	argStructFields := make([]reflect.StructField, inT)
	for i := 0; i < inT; i++ {
		argStructFields[i] = reflect.StructField{
			Name: fmt.Sprintf("Arg%d", i),
			Type: typ.In(i),
		}
	}

	argStructType := reflect.StructOf(argStructFields)
	logger.Debugw("created args struct type", "type", argStructType)

	argChanType := reflect.ChanOf(reflect.BothDir, argStructType)
	argChan := reflect.MakeChan(argChanType, 1)

	logger.Debugw("making callback outputs", "count", outT)
	retStuctFields := make([]reflect.StructField, outT)
	for i := 0; i < outT; i++ {
		retStuctFields[i] = reflect.StructField{
			Name: fmt.Sprintf("Ret%d", i),
			Type: typ.Out(i),
		}
	}

	retStructType := reflect.StructOf(retStuctFields)
	logger.Debugw("created return struct type", "type", retStructType)

	retChanType := reflect.ChanOf(reflect.BothDir, retStructType)
	retChan := reflect.MakeChan(retChanType, 1)

	mu := sync.Mutex{}

	wrappedFunc := reflect.MakeFunc(typ, func(args []reflect.Value) (results []reflect.Value) {
		logger.Debugw("invoked wrapped function", "argCount", len(args), "args", args)
		// mu.Lock()
		argStruct := reflect.New(argStructType).Elem()
		for i, arg := range args {
			argStruct.Field(i).Set(arg)
		}
		logger.Debugw("sending args struct", "args", argStruct)
		argChan.Send(argStruct)
		recv, ok := retChan.Recv()
		if !ok {
			results = make([]reflect.Value, outT)
			return results
		}

		retStruct := recv
		results = make([]reflect.Value, outT)
		for i := 0; i < outT; i++ {
			results[i] = retStruct.Field(i)
		}
		return results
	})

	logger.Debugw("created wrapped function", "func", wrappedFunc)

	return wrappedFunc.Interface().(Callback), &mu, argChan, retChan
}

func HandleCallback[Callback any](ctx context.Context, loop *EventLoop, cb Callback, mu *sync.Mutex, args, ret reflect.Value) {
	val := reflect.ValueOf(cb)
	typ := reflect.TypeOf(cb)

	if typ.Kind() != reflect.Func {
		return
	}

	inT := typ.NumIn()
	outT := typ.NumOut()

	loop.Register(CallbackHandler{
		loop: loop,
		cb:   val,
		args: args,
		ret:  ret,
		inT:  inT,
		outT: outT,
		done: reflect.ValueOf(ctx.Done()),
		mu:   mu,
	})
}

func RegisterCallback[Callback any](ctx context.Context, loop *EventLoop, cb Callback) Callback {
	wrap, mu, args, ret := MakeCallback[Callback]()

	HandleCallback(ctx, loop, cb, mu, args, ret)

	return wrap
}
