package sipbin

import (
	"fmt"
	"sync"
)

var ErrTransactionClosed = fmt.Errorf("transaction closed")

func NewSipTransaction() *SipTransaction {
	t := &SipTransaction{}
	t.cond = sync.NewCond(&t.mu)
	t.pending = true
	return t
}

type SipTransaction struct {
	mu      sync.Mutex
	cond    *sync.Cond
	active  bool
	pending bool
	closed  bool
}

func (t *SipTransaction) WaitReady() (unlock func(), err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrTransactionClosed
	}
	for t.active {
		t.cond.Wait()
		if t.closed {
			t.mu.Unlock()
			return nil, ErrTransactionClosed
		}
	}
	t.active = true
	t.pending = false
	return func() {
		if !t.pending {
			t.active = false
			t.cond.Broadcast()
		}
		t.mu.Unlock()
	}, nil
}

func (t *SipTransaction) SetPending() {
	// do we need to handle ack timeout here?
	t.pending = true
}

func (t *SipTransaction) Ack() (unlock func()) {
	t.mu.Lock()
	return func() {
		t.active = false
		t.cond.Broadcast()
		t.mu.Unlock()
	}
}

func (t *SipTransaction) IsPending() (pending bool, unlock func()) {
	t.mu.Lock()
	pending = t.active
	return pending, func() {
		t.mu.Unlock()
	}
}

func (t *SipTransaction) Close() {
	t.mu.Lock()
	t.closed = true
	t.cond.Broadcast()
	t.mu.Unlock()
}
