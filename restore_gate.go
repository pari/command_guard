package main

import (
	"fmt"
	"sync"
)

type restoreGate struct {
	mu        sync.Mutex
	restoring bool
	active    int
}

var configRestoreGate = &restoreGate{}

func (g *restoreGate) BeginWork() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.restoring {
		return fmt.Errorf("configuration restore is in progress")
	}
	g.active++
	return nil
}

func (g *restoreGate) EndWork() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.mu.Unlock()
}

func (g *restoreGate) BeginRestore() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.restoring {
		return fmt.Errorf("configuration restore is already in progress")
	}
	if g.active > 0 {
		return fmt.Errorf("cannot restore while %d command or RunBook execution(s) are active", g.active)
	}
	if execManager != nil && execManager.getActiveRequestCount() > 0 {
		return fmt.Errorf("cannot restore while command executions are active")
	}
	if runBookManager != nil && len(runBookManager.ActiveRuns()) > 0 {
		return fmt.Errorf("cannot restore while RunBooks are active")
	}
	g.restoring = true
	return nil
}

func (g *restoreGate) EndRestore() {
	g.mu.Lock()
	g.restoring = false
	g.mu.Unlock()
}
