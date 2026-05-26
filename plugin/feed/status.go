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

// OrchestratorStatusSnapshot is a lock-free copy returned to callers.
type OrchestratorStatusSnapshot struct {
	LastTickAt  time.Time
	LastUserMap map[string]time.Time
}

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

// orchestratorGetStatus returns a snapshot of the current orchestrator status.
func orchestratorGetStatus() OrchestratorStatusSnapshot {
	globalOrchestratorStatus.mu.RLock()
	defer globalOrchestratorStatus.mu.RUnlock()
	cp := OrchestratorStatusSnapshot{
		LastTickAt:  globalOrchestratorStatus.LastTickAt,
		LastUserMap: make(map[string]time.Time, len(globalOrchestratorStatus.LastUserMap)),
	}
	for k, v := range globalOrchestratorStatus.LastUserMap {
		cp.LastUserMap[k] = v
	}
	return cp
}
