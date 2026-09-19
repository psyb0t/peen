package worker

import "github.com/google/uuid"

// WorkerCommand is the subcommand a worker process runs.
//
// Native and Docker launches both run this exact subcommand of the same Peen
// binary, so a worker is the same program in both forms. It is not a general
// user command: without a controller-issued launch document on stdin it has no
// session, no endpoint, and nothing to do.
const WorkerCommand = "worker"

// Docker worker identity. The controller only ever stops or removes a container
// whose stored ID still carries exactly these labels, so an unrelated container
// on the same host is never touched.
const (
	// containerNamePrefix makes a worker container's name deterministic.
	containerNamePrefix = "peen-worker-"

	// LabelManaged marks a container this Peen controller created.
	LabelManaged = "peen.managed"

	// LabelManagedValue is the only accepted value for LabelManaged.
	LabelManagedValue = "true"

	// LabelSession names the one session a worker container serves.
	LabelSession = "peen.session"
)

// ContainerName is the deterministic name of one session's worker container.
//
// It is derived from the session alone, so a controller that restarts can find
// the container it left behind without storing a second identifier.
func ContainerName(sessionID uuid.UUID) string {
	return containerNamePrefix + sessionID.String()
}

// ContainerLabels are the only labels a worker container carries.
func ContainerLabels(sessionID uuid.UUID) map[string]string {
	return map[string]string{
		LabelManaged: LabelManagedValue,
		LabelSession: sessionID.String(),
	}
}

// OwnedBy reports whether a container's labels mark it as this controller's
// worker for one session.
//
// Cleanup and restart recovery both go through this. Combined with a stored
// container ID it is what keeps the controller from ever acting on a container
// it did not create.
func OwnedBy(labels map[string]string, sessionID uuid.UUID) bool {
	if labels == nil {
		return false
	}

	return labels[LabelManaged] == LabelManagedValue &&
		labels[LabelSession] == sessionID.String()
}
