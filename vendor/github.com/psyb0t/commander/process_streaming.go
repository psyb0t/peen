package commander

import (
	"log/slog"
	"time"
)

const (
	// channelSendTimeout is the maximum time to wait when sending to a channel
	// before considering it blocked and marking it as nil
	channelSendTimeout = 100 * time.Millisecond
)

// Stream sends live output to separate stdout and stderr channels
func (p *process) Stream(stdout, stderr chan<- string) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()

	// Add channels to the list of active streams
	p.streamChans = append(p.streamChans, streamChannels{
		stdout: stdout,
		stderr: stderr,
	})

	slog.Debug("added new stream channels", "totalStreams", len(p.streamChans))
}

// discardInternalOutput continuously drains internal channels
// to prevent blocking
// Only sends to user channels if they exist, otherwise discards everything
func (p *process) discardInternalOutput() {
	slog.Debug("starting output discard goroutine")

	defer func() {
		slog.Debug(
			"output discard goroutine finishing, closing stream channels",
		)
		p.closeStreamChannels()
	}()

	stdoutCount := 0
	stderrCount := 0

	for {
		select {
		case <-p.doneCh:
			slog.Debug("output discard goroutine stopping",
				"stdoutLines", stdoutCount,
				"stderrLines", stderrCount,
			)

			return

		case line, ok := <-p.internalStdout:
			if !ok {
				slog.Debug("stdout channel closed, draining stderr",
					"stdoutLines", stdoutCount,
				)
				p.drainStderr()

				return
			}

			stdoutCount++

			p.broadcastToStdout(line)

		case line, ok := <-p.internalStderr:
			if !ok {
				slog.Debug("stderr channel closed",
					"stderrLines", stderrCount,
				)

				continue
			}

			stderrCount++

			p.broadcastToStderr(line)
		}
	}
}

// drainStderr drains remaining stderr after stdout closes
func (p *process) drainStderr() {
	for {
		select {
		case <-p.doneCh:
			return
		case _, ok := <-p.internalStderr:
			if !ok {
				return
			}
		}
	}
}

// broadcastToStdout sends line to stdout channels if they exist
func (p *process) broadcastToStdout(line string) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()

	if len(p.streamChans) == 0 {
		return
	}

	// Send to all stdout channels
	for i := len(p.streamChans) - 1; i >= 0; i-- {
		channels := p.streamChans[i]
		if channels.stdout != nil {
			// Safely send to channel with recover to handle send-on-closed-channel panic
			func() {
				defer func() {
					if recover() != nil {
						// Channel was closed, mark it as nil
						p.streamChans[i].stdout = nil
					}
				}()

				select {
				case channels.stdout <- line:
					// Successfully sent
				case <-time.After(channelSendTimeout):
					// Channel is blocked for too long, mark stdout as nil
					p.streamChans[i].stdout = nil
				}
			}()
		}
	}
}

// broadcastToStderr sends line to stderr channels if they exist
func (p *process) broadcastToStderr(line string) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()

	if len(p.streamChans) == 0 {
		return
	}

	// Send to all stderr channels
	for i := len(p.streamChans) - 1; i >= 0; i-- {
		channels := p.streamChans[i]
		if channels.stderr != nil {
			// Safely send to channel with recover to handle send-on-closed-channel panic
			func() {
				defer func() {
					if recover() != nil {
						// Channel was closed, mark it as nil
						p.streamChans[i].stderr = nil
					}
				}()

				select {
				case channels.stderr <- line:
					// Successfully sent
				case <-time.After(channelSendTimeout):
					// Channel is blocked for too long, mark stderr as nil
					p.streamChans[i].stderr = nil
				}
			}()
		}
	}
}

// closeStreamChannels closes all active stream channels safely
func (p *process) closeStreamChannels() {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()

	// If channels are already closed, return early
	if p.streamChans == nil {
		return
	}

	for _, channels := range p.streamChans {
		if channels.stdout != nil {
			// Safely close channel with recover to handle double-close panic
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Channel was already closed, ignore the panic
						slog.Warn("channel already closed", "recover", r)
					}
				}()

				close(channels.stdout)
			}()
		}

		if channels.stderr != nil {
			// Safely close channel with recover to handle double-close panic
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Channel was already closed, ignore the panic
						slog.Warn("channel already closed", "recover", r)
					}
				}()

				close(channels.stderr)
			}()
		}
	}

	p.streamChans = nil
}
