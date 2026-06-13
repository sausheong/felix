package gateway

const (
	maxConnections    = 64 // concurrent WebSocket connections
	maxConcurrentRuns = 8  // concurrent chat.send agent runs
)

// initLimits lazily creates the run semaphore. Safe to call multiple times;
// the connection counter is a plain atomic and needs no init.
func (h *WebSocketHandler) initLimits() {
	if h.runSem == nil {
		h.runSem = make(chan struct{}, maxConcurrentRuns)
	}
}

func (h *WebSocketHandler) acquireRun() bool {
	select {
	case h.runSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *WebSocketHandler) releaseRun() {
	select {
	case <-h.runSem:
	default:
	}
}

func (h *WebSocketHandler) acquireConn() bool {
	if h.connCount.Add(1) > maxConnections {
		h.connCount.Add(-1)
		return false
	}
	return true
}

func (h *WebSocketHandler) releaseConn() { h.connCount.Add(-1) }
