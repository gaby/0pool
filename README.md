# pool

Common Go connection pool inspired by `sync.Pool` but designed for long-lived resources (TCP clients, etc.).

## Features

### Methods
- `Get`
- `Put`
- `Len`
- `Destroy`

### Attributes
- `New`
- `Ping`
- `Close`

> You should set `pool.New` and `pool.Close` functions when building a pool.

## Getting Started

Install:

```
go get -u github.com/go-baa/pool
```

## Usage

### Basic TCP pool
```go
package main

import (
    "log"
    "net"

    "github.com/go-baa/pool"
)

func main() {
    // create pool with initial capacity 2 and maximum 10
    pl, err := pool.New(2, 10, func() any {
        addr, _ := net.ResolveTCPAddr("tcp4", "127.0.0.1:8003")
        conn, err := net.DialTCP("tcp4", nil, addr)
        if err != nil {
            log.Fatalf("create client connection error: %v", err)
        }
        return conn
    })
    if err != nil {
        log.Fatalf("create pool error: %v", err)
    }

    // validate a connection before handing it out
    pl.Ping = func(conn any) bool {
        // check connection status
        return true
    }

    // clean up a connection before dropping it
    pl.Close = func(conn any) {
        conn.(*net.TCPConn).Close()
    }

    // get connection from pool
    c, err := pl.Get()
    if err != nil {
        log.Fatalf("get client error: %v", err)
    }

    conn := c.(*net.TCPConn)
    if _, err = conn.Write([]byte("PING")); err != nil {
        log.Fatalf("write error: %v", err)
    }

    result := make([]byte, 4)
    if _, err = conn.Read(result); err != nil {
        log.Fatalf("read data error: %v", err)
    }
    log.Printf("got data: %s", result)

    // put back for reuse
    pl.Put(conn)

    // pool size (number of idle items)
    log.Printf("idle connections: %d", pl.Len())

    // destroy closes all idle connections and prevents further use
    pl.Destroy()
}
```

### Handling closed pools
If a pool has been destroyed, `Get` returns `pool.ErrClosed` and nothing new is created (remember to import `errors`):

```go
import "errors"

if _, err := pl.Get(); errors.Is(err, pool.ErrClosed) {
    // handle closed pool
}
```

You can find test server code in `pool_test.go`.
