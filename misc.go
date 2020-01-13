package main

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"
)

type sharedWriter struct {
	sync.Mutex

	writer io.Writer
}

var _ io.Writer = (*sharedWriter)(nil)

func (sw *sharedWriter) Write(p []byte) (n int, err error) {
	sw.Lock()
	defer sw.Unlock()

	return sw.writer.Write(p)
}

// critical can be locked non-exclusively to delay shutdown.
var critical sync.RWMutex

// shuttingDown is closed on shutdown.
var shuttingDown = make(chan struct{})

var background = context.Background()

// time2Float returns the same as t.Unix(), but as float64.
func time2Float(t time.Time) float64 {
	ts := t.Unix()
	return float64(ts) + float64(t.Sub(time.Unix(ts, 0)))/float64(time.Second)
}

func waitForCmd(cmd *exec.Cmd, out chan<- error) {
	out <- cmd.Wait()
}
