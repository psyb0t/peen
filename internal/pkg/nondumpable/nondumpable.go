// Package nondumpable keeps a Peen process's memory and environment away from
// other processes that run as the same user.
//
// A `control` agent runs as the controller's user. Without this, a command it
// runs could read the controller's API token and the provider credentials from
// /proc/<pid>/environ of the controller or of its own worker.
package nondumpable
