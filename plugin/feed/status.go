package feed

import (
	"sync"
	"time"
)

// OrchestratorStatus holds observable state from the background orchestrator.
// It is safe for concurrent use via its internal mutex.
type OrchestratorStatus struct {
	mu          sync.RWMutex
	LastTickAt  time.Time
	LastUserMap map[string]time.Time // username -> last processed
}

// globalOrchestratorStatus is the singleton status updated by the orchestrator.
var globalOrchestratorStatus = &OrchestratorStatus{LastUserMap: make(map[string]time.Time)}

// RecordTick updates the last tick time. Called by the orchestrator on each full pass.
func RecordTick() {
	globalOrchestratorStatus.mu.Lock()
	defer globalOrchestratorStatus.mu.Unlock()
	globalOrchestratorStatus.LastTickAt = time.Now()
}

// RecordUserProcessed updates the last-processed time for a user.
func RecordUserProcessed(username string) {
	globalOrchestratorStatus.mu.Lock()
	defer globalOrchestratorStatus.mu.Unlock()
	globalOrchestratorStatus.LastUserMap[username] = time.Now()
}
