package pool

import (
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrClosed is the error resulting if the pool is closed via pool.Close().
	ErrClosed = errors.New("pool is closed")
)

// Pool common connection pool
type Pool struct {
	// New create connection function
	New func() any
	// Ping check connection is ok
	Ping func(any) bool
	// Close close connection
	Close  func(any)
	store  chan any
	mu     sync.Mutex
	closed bool
	maxCap int
	total  int
}

// New create a pool with capacity
func New(initCap, maxCap int, newFunc func() any, opts ...func(*Pool)) (*Pool, error) {
	if maxCap <= 0 || initCap < 0 || initCap > maxCap {
		return nil, fmt.Errorf("invalid capacity settings")
	}
	p := new(Pool)
	p.store = make(chan any, maxCap)
	p.maxCap = maxCap
	if newFunc != nil {
		p.New = newFunc
	}

	for _, opt := range opts {
		opt(p)
	}

	created := make([]any, 0, initCap)
	for i := 0; i < initCap; i++ {
		v, err := p.create()
		if err != nil {
			p.closed = true
			close(p.store)
			for _, seeded := range created {
				p.closeValue(seeded)
			}
			p.store = nil
			p.total = 0
			return nil, err
		}
		created = append(created, v)
		p.total++
		p.store <- v
	}
	return p, nil
}

// Len returns current connections in pool
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.store == nil {
		return 0
	}

	return len(p.store)
}

// Get returns a conn from store or create one
func (p *Pool) Get() (any, error) {
	p.mu.Lock()
	if p.store == nil || p.closed {
		p.mu.Unlock()
		// pool already destroyed, returns error
		return nil, ErrClosed
	}
	for {
		store := p.store
		closed := p.closed || store == nil
		if closed {
			p.mu.Unlock()
			return nil, ErrClosed
		}

		select {
		case v, ok := <-store:
			p.mu.Unlock()
			if !ok {
				return nil, ErrClosed
			}
			if p.Ping != nil && !p.Ping(v) {
				p.closeValue(v)
				p.decrementTotal()
				p.mu.Lock()
				continue
			}
			return v, nil
		default:
			if p.total >= p.maxCap {
				p.mu.Unlock()
				v, ok := <-store
				if !ok {
					return nil, ErrClosed
				}
				if p.Ping != nil && !p.Ping(v) {
					p.closeValue(v)
					p.decrementTotal()
					p.mu.Lock()
					continue
				}
				return v, nil
			}
			p.total++
			p.mu.Unlock()

			v, err := p.create()
			if err != nil {
				p.decrementTotal()
				return nil, err
			}
			if p.Ping != nil && !p.Ping(v) {
				p.closeValue(v)
				p.decrementTotal()
				p.mu.Lock()
				continue
			}

			p.mu.Lock()
			if p.closed || p.store == nil {
				p.mu.Unlock()
				p.closeValue(v)
				p.decrementTotal()
				return nil, ErrClosed
			}
			p.mu.Unlock()
			return v, nil
		}
	}
}

// Put set back conn into store again
func (p *Pool) Put(v any) {
	p.mu.Lock()
	if p.store == nil || p.closed {
		if p.total > 0 {
			p.total--
		}
		p.mu.Unlock()
		p.closeValue(v)
		return
	}
	select {
	case p.store <- v:
		p.mu.Unlock()
		return
	default:
		// pool is full, close passed connection
		if p.total > 0 {
			p.total--
		}
		p.mu.Unlock()
		p.closeValue(v)
		return
	}
}

// Destroy clear all connections
func (p *Pool) Destroy() {
	p.mu.Lock()
	if p.store == nil || p.closed {
		p.mu.Unlock()
		// pool already destroyed
		return
	}
	p.closed = true
	store := p.store
	p.store = nil
	p.total = 0
	p.mu.Unlock()

	close(store)
	for v := range store {
		if p.Close != nil {
			p.Close(v)
		}
	}
}

func (p *Pool) create() (any, error) {
	if p.New == nil {
		return nil, fmt.Errorf("Pool.New is nil, can not create connection")
	}

	var (
		v   any
		err error
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("Pool.New panic: %v", r)
			}
		}()
		v = p.New()
	}()

	return v, err
}

func (p *Pool) closeValue(v any) {
	if p.Close != nil {
		p.Close(v)
	}
}

func (p *Pool) decrementTotal() {
	p.mu.Lock()
	if p.total > 0 {
		p.total--
	}
	p.mu.Unlock()
}
