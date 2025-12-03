package pool

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewPoolValidation(t *testing.T) {
	p, err := New(2, 1, func() any { return nil })
	require.Nil(t, p)
	require.Error(t, err)
}

func TestNewPoolRejectsNegativeCapacities(t *testing.T) {
	for name, tc := range map[string]struct {
		initCap int
		maxCap  int
	}{
		"negative_init": {initCap: -1, maxCap: 1},
		"negative_max":  {initCap: 1, maxCap: -1},
		"both_negative": {initCap: -1, maxCap: -1},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := New(tc.initCap, tc.maxCap, func() any { return nil })
			require.Nil(t, p)
			require.Error(t, err)
			require.EqualError(t, err, "invalid capacity settings")
		})
	}
}

func TestNewPoolCreatesInitialConnections(t *testing.T) {
	created := 0
	p, err := New(2, 4, func() any {
		created++
		return created
	})
	require.NoError(t, err)
	require.Equal(t, 2, p.Len())
	require.Equal(t, 2, created)
}

func TestNewPoolFailsWhenCreatorMissing(t *testing.T) {
	p, err := New(1, 1, nil)
	require.Nil(t, p)
	require.Error(t, err)
}

func TestNewPoolCleansUpOnInitFailure(t *testing.T) {
	var closed atomic.Int32
	var poolRef *Pool

	creatorCalls := 0
	constructor := func() any {
		creatorCalls++
		if creatorCalls == 1 {
			return fmt.Sprintf("conn-%d", creatorCalls)
		}

		// Force a panic to simulate a failing constructor after some items were created.
		if poolRef != nil {
			poolRef.New = nil
		}
		panic("constructor failed")
	}

	pool, err := New(2, 2, constructor, func(p *Pool) {
		poolRef = p
		p.Close = func(v any) {
			_ = v
			closed.Add(1)
		}
	})

	require.Nil(t, pool)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Pool.New panic: constructor failed")
	require.Equal(t, 2, creatorCalls)
	require.EqualValues(t, 1, closed.Load())
}

func TestGetAndPingValidation(t *testing.T) {
	created := 0
	p, err := New(1, 2, func() any {
		created++
		return created
	})
	require.NoError(t, err)

	// First value should be discarded because Ping returns false.
	p.Ping = func(v any) bool {
		return v.(int) != 1
	}

	v, err := p.Get()
	require.NoError(t, err)
	require.Equal(t, 2, v)
	require.Equal(t, 2, created)
}

func TestGetClosesUnhealthyValues(t *testing.T) {
	created := 0
	p, err := New(1, 1, func() any {
		created++
		if created == 1 {
			return "bad"
		}
		return "replacement"
	})
	require.NoError(t, err)

	closed := 0
	p.Close = func(v any) {
		if v == "bad" {
			closed++
		}
	}

	p.Ping = func(v any) bool {
		return v != "bad"
	}

	v, err := p.Get()
	require.NoError(t, err)
	require.Equal(t, "replacement", v)
	require.Equal(t, 1, closed)
	require.Equal(t, 2, created)
}

func TestGetReturnsCachedItemWhenHealthy(t *testing.T) {
	p, err := New(1, 1, func() any { return "cached" })
	require.NoError(t, err)

	v, err := p.Get()
	require.NoError(t, err)
	require.Equal(t, "cached", v)
}

func TestGetWithoutNew(t *testing.T) {
	p, err := New(0, 1, nil)
	require.NoError(t, err)

	v, err := p.Get()
	require.Error(t, err)
	require.Nil(t, v)
	require.EqualError(t, err, "Pool.New is nil, can not create connection")
}

func TestPutAndOverflow(t *testing.T) {
	closed := 0
	p, err := New(1, 1, func() any { return "resource" })
	require.NoError(t, err)

	p.Close = func(v any) {
		if v == "overflow" {
			closed++
		}
	}

	p.Put("overflow")
	require.Equal(t, 1, closed)
	require.Equal(t, 1, p.Len())
}

func TestPutAddsItemWhenCapacityAvailable(t *testing.T) {
	p, err := New(0, 1, func() any { return "new" })
	require.NoError(t, err)

	p.Put("stored")
	require.Equal(t, 1, p.Len())
}

func TestDestroyAndGetAfterClose(t *testing.T) {
	closed := 0
	p, err := New(2, 2, func() any { return "keep" })
	require.NoError(t, err)

	p.Close = func(v any) {
		if v == "keep" {
			closed++
		}
	}

	p.Destroy()
	require.Equal(t, 2, closed)
	require.Equal(t, 0, p.Len())

	// Calling destroy twice is safe and should be a no-op.
	p.Destroy()
	require.Equal(t, 2, closed)

	v, err := p.Get()
	require.Nil(t, v)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrClosed))
}

func TestDestroyBetweenUnlockAndCreate(t *testing.T) {
	created := 0
	startCreate := make(chan struct{})
	finishCreate := make(chan struct{})
	closed := 0

	p, err := New(0, 1, func() any {
		created++
		close(startCreate)
		<-finishCreate
		return fmt.Sprintf("conn-%d", created)
	})
	require.NoError(t, err)

	p.Close = func(v any) {
		closed++
	}

	result := make(chan struct{})
	var got any
	var getErr error
	go func() {
		defer close(result)
		got, getErr = p.Get()
	}()

	<-startCreate
	p.Destroy()
	close(finishCreate)
	<-result

	require.Nil(t, got)
	require.Error(t, getErr)
	require.True(t, errors.Is(getErr, ErrClosed))
	require.Equal(t, 1, created)
	require.Equal(t, 1, closed)
	require.Equal(t, 0, p.Len())
}

func TestDestroyBeforeGetDoesNotCreate(t *testing.T) {
	created := 0
	p, err := New(0, 1, func() any {
		created++
		return created
	})
	require.NoError(t, err)

	p.Destroy()
	v, err := p.Get()
	require.Nil(t, v)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrClosed))
	require.Equal(t, 0, created)
}

func TestDestroyAndPutConcurrent(t *testing.T) {
	p, err := New(1, 1, func() any { return "initial" })
	require.NoError(t, err)

	var wg sync.WaitGroup
	var closedMu sync.Mutex
	closed := 0
	p.Close = func(v any) {
		closedMu.Lock()
		closed++
		closedMu.Unlock()
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		p.Destroy()
	}()

	go func() {
		defer wg.Done()
		p.Put("late")
	}()

	wg.Wait()

	require.Equal(t, 0, p.Len())

	closedMu.Lock()
	defer closedMu.Unlock()
	require.Equal(t, 2, closed)

	v, err := p.Get()
	require.Nil(t, v)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrClosed))
}

func TestLenIsSafeDuringDestroy(t *testing.T) {
	p, err := New(1, 1, func() any { return "val" })
	require.NoError(t, err)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = p.Len()
			}
		}
	}()

	time.Sleep(10 * time.Millisecond)
	p.Destroy()
	close(done)
	wg.Wait()

	require.Equal(t, 0, p.Len())
}

func TestGetReturnsErrClosedWhenDestroyedMidflight(t *testing.T) {
	created := 0
	p, err := New(1, 1, func() any {
		created++
		return created
	})
	require.NoError(t, err)

	startDestroy := make(chan struct{})
	destroyed := make(chan struct{})

	p.Ping = func(v any) bool {
		if v.(int) == 1 {
			close(startDestroy)
			<-destroyed
			return false
		}
		return true
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startDestroy
		p.Destroy()
		close(destroyed)
	}()

	v, err := p.Get()
	require.Nil(t, v)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrClosed))
	require.Equal(t, 1, created)

	wg.Wait()
}

func TestPutAfterDestroyClosesValue(t *testing.T) {
	p, err := New(1, 1, func() any { return "existing" })
	require.NoError(t, err)

	closed := 0
	p.Close = func(v any) {
		if v == "existing" || v == "later" {
			closed++
		}
	}

	p.Destroy()
	p.Put("later")

	require.Equal(t, 0, p.Len())
	require.Equal(t, 2, closed)
}

func TestGetDoesNotExceedMaxCap(t *testing.T) {
	const maxCap = 3
	var created atomic.Int32
	p, err := New(0, maxCap, func() any {
		return fmt.Sprintf("conn-%d", created.Add(1))
	})
	require.NoError(t, err)

	hold := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < maxCap; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			v, err := p.Get()
			require.NoError(t, err)
			require.NotNil(t, v)

			<-hold
			p.Put(v)
		}()
	}

	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, maxCap, created.Load())

	blockedReady := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(blockedReady)
		v, err := p.Get()
		require.NoError(t, err)
		require.NotNil(t, v)
		p.Put(v)
		close(done)
	}()

	<-blockedReady
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, maxCap, created.Load())

	close(hold)
	wg.Wait()
	<-done
	require.EqualValues(t, maxCap, created.Load())
}
