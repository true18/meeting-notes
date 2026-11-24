package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &testPool{started: make(chan struct{}), done: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- runWorker(ctx, pool, time.Second) }()
	<-pool.started
	cancel()
	close(pool.done)
	if err := <-result; err != nil {
		t.Fatalf("runWorker: %v", err)
	}
}

func TestRunWorkerTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &testPool{started: make(chan struct{}), done: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- runWorker(ctx, pool, 10*time.Millisecond) }()
	<-pool.started
	cancel()
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runWorker error = %v", err)
	}
	close(pool.done)
}

type testPool struct {
	started chan struct{}
	done    chan struct{}
}

func (p *testPool) Run(context.Context) error {
	close(p.started)
	<-p.done
	return nil
}
