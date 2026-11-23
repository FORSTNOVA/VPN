package main

import (
	"sync"
	"testing"
	"time"
)

// Synchronization model only; no real process or kernel callback is invoked.
func TestMigrationExitSafeWatcherOrderingModel(t *testing.T) {
	var mu sync.Mutex
	exited, attempt, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	mu.Lock()
	go func() { close(exited); close(attempt); mu.Lock(); mu.Unlock(); close(done) }()
	select {
	case <-attempt:
	case <-time.After(5 * time.Second):
		mu.Unlock()
		t.Fatal("watcher did not reach callback")
	}
	select {
	case <-exited:
	default:
		mu.Unlock()
		t.Fatal("exit signal must precede callback lock")
	}
	select {
	case <-done:
		mu.Unlock()
		t.Fatal("callback bypassed mutex")
	default:
	}
	mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback failed to resume")
	}
}
