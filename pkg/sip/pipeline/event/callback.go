package event

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

func MakeCallback[Callback any]() (Callback, *sync.Mutex, reflect.Value, reflect.Value) {
	typ := reflect.TypeFor[Callback]()

	fmt.Printf("Making callback of type: %v\n", typ)

	if typ.Kind() != reflect.Func {
		return *new(Callback), &sync.Mutex{}, reflect.Value{}, reflect.Value{}
	}

	inT := typ.NumIn()
	outT := typ.NumOut()

	fmt.Printf("Making callback with %d inputs\n", inT)
	argStructFields := make([]reflect.StructField, inT)
	for i := 0; i < inT; i++ {
		argStructFields[i] = reflect.StructField{
			Name: fmt.Sprintf("Arg%d", i),
			Type: typ.In(i),
		}
	}

	argStructType := reflect.StructOf(argStructFields)
	fmt.Printf("Created args struct type: %v\n", argStructType)

	argChanType := reflect.ChanOf(reflect.BothDir, argStructType)
	argChan := reflect.MakeChan(argChanType, 1)

	fmt.Printf("Making callback with %d outputs\n", outT)
	retStuctFields := make([]reflect.StructField, outT)
	for i := 0; i < outT; i++ {
		retStuctFields[i] = reflect.StructField{
			Name: fmt.Sprintf("Ret%d", i),
			Type: typ.Out(i),
		}
	}

	retStructType := reflect.StructOf(retStuctFields)
	fmt.Printf("Created return struct type: %v\n", retStructType)

	retChanType := reflect.ChanOf(reflect.BothDir, retStructType)
	retChan := reflect.MakeChan(retChanType, 1)

	mu := sync.Mutex{}

	wrappedFunc := reflect.MakeFunc(typ, func(args []reflect.Value) (results []reflect.Value) {
		fmt.Printf("Invoked wrapped function with %d args: %v\n", len(args), args)
		// mu.Lock()
		argStruct := reflect.New(argStructType).Elem()
		for i, arg := range args {
			argStruct.Field(i).Set(arg)
		}
		fmt.Printf("Sending args struct: %v\n", argStruct)
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

	fmt.Printf("Created wrapped function: %v\n", wrappedFunc)

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
