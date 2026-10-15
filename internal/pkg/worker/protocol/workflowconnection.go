package protocol

// Workflows binds the registered session to the existing private transport.
func (c *Connection) Workflows() *WorkflowBackend {
	return NewWorkflowBackend(c.conn, c.Ack.SessionID)
}
