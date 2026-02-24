package livekitbin

import (
	"fmt"
	"sync"
	"sync/atomic"
)

type RoomState int64

const (
	RoomStateNone    RoomState = 0
	RoomStateJoined  RoomState = 2
	RoomStateJoining RoomState = 4
	RoomStateClosed  RoomState = 8
)

func (s RoomState) String() string {
	switch s {
	case RoomStateNone:
		return "none"
	case RoomStateJoined:
		return "joined"
	case RoomStateJoining:
		return "joining"
	case RoomStateClosed:
		return "closed"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

type state struct {
	mu    sync.Mutex
	state atomic.Int64
	cond  *sync.Cond
	wg    sync.WaitGroup
}

func (s *state) Is(state RoomState) bool {
	return (s.state.Load() & int64(state)) != 0
}

func (s *state) Set(state RoomState) RoomState {
	old := s.state.Or(int64(state))
	s.cond.Broadcast()
	return RoomState(old)
}

func (s *state) Unset(state RoomState) RoomState {
	old := s.state.And(^int64(state))
	s.cond.Broadcast()
	return RoomState(old)
}

func (s *state) Wait(state RoomState) error {
	var current int64
	for current = s.state.Load(); (current&int64(state)) == 0 && (current&int64(RoomStateClosed)) == 0; current = s.state.Load() {
		s.cond.Wait()
	}
	if (current & int64(state)) != 0 {
		return nil
	}
	return fmt.Errorf("room closed while waiting for state %d", state)
}
