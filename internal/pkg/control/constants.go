package control

// Service names for the two control services. They live here rather than in
// either service because the dependency edge between them needs the other's
// name, and a service never imports a sibling service.
const (
	// CoreServiceName opens configuration, SQLite, the provider registry, the
	// workspace policy, and the agent runtime.
	CoreServiceName = "control-core"

	// APIServiceName serves REST, the global WebSocket feed, and metrics over
	// what CoreServiceName opened.
	APIServiceName = "control-api"
)
