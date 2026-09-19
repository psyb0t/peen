package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	// credentialBytes is the entropy behind one worker credential. It is a
	// single-use secret for one generation, not a password, so 256 bits is
	// well past what an attacker who cannot read the socket could guess.
	credentialBytes = 32

	// socketFileName is the socket inside one session's own socket directory.
	// The directory carries the session identity, so the file does not repeat
	// it.
	socketFileName = "worker.sock"

	// maxSocketPathBytes is what a Unix socket address can hold. The kernel's
	// sun_path is 108 bytes including its terminator, and it does not grow with
	// the filesystem's path limit, so a socket root that fits every other path
	// check can still be too deep for a socket.
	maxSocketPathBytes = 107
)

// Credential is a worker's one-time proof that its controller launched it.
//
// The raw value exists only in memory and in the launch document. SQLite
// stores the hash, so a controller that reads its own database back cannot
// recover a credential, and a restarted controller can still accept the worker
// it recorded when that worker reconnects.
type Credential string

// NewCredential mints one generation's credential.
func NewCredential() (Credential, error) {
	raw := make([]byte, credentialBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", ctxerrors.Wrap(err, "generate worker credential")
	}

	return Credential(base64.RawURLEncoding.EncodeToString(raw)), nil
}

// Hash is the verification digest stored with the generation.
func (c Credential) Hash() string {
	sum := sha256.Sum256([]byte(c))

	return hex.EncodeToString(sum[:])
}

// Matches reports whether this credential verifies against a stored hash.
//
// The comparison is constant time, so a worker cannot learn a hash one byte at
// a time by timing its own rejections.
func (c Credential) Matches(storedHash string) bool {
	if storedHash == "" {
		return false
	}

	return subtle.ConstantTimeCompare(
		[]byte(c.Hash()),
		[]byte(storedHash),
	) == 1
}

// LaunchDocument is everything the controller tells one worker at launch.
//
// It is the entire worker contract. A worker that starts without one fails,
// because `peen worker` is not a general user command: it cannot choose its
// session, its endpoint, or its workspace.
type LaunchDocument struct {
	SessionID    uuid.UUID  `json:"sessionId"`
	GenerationID uuid.UUID  `json:"generationId"`
	Credential   Credential `json:"credential"`

	// SocketPath is the worker's private, session-bound Unix socket. It lives
	// in this session's own directory beneath the socket root, so a Docker
	// worker receives that one directory and never the root. It is the only
	// controller surface the worker has, and it carries no generic control API
	// credential.
	SocketPath string `json:"socketPath"`

	// Workspace is the literal directory the worker runs in.
	Workspace string `json:"workspace"`

	// ConfigDirectory is the global Peen configuration, read-only in a Docker
	// worker. Controller runtime state lives under PEEN_STATE_DIR instead, and
	// the controller refuses to start if that directory sits inside this one,
	// so the database is never reachable through this mount.
	ConfigDirectory string `json:"configDirectory"`

	// Profile names the operator-defined profile this generation runs under.
	// The worker reports it back; it cannot choose another.
	Profile string `json:"profile"`
}

// Validate rejects a launch document a worker cannot act on.
func (d LaunchDocument) Validate() error {
	if d.SessionID == uuid.Nil || d.GenerationID == uuid.Nil {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker launch requires a session and a generation",
		)
	}

	if d.Credential == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker launch requires a credential",
		)
	}

	if !filepath.IsAbs(d.SocketPath) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker launch requires an absolute socket path",
		)
	}

	if !filepath.IsAbs(d.Workspace) || !filepath.IsAbs(d.ConfigDirectory) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker launch requires absolute workspace and config paths",
		)
	}

	if d.Profile == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker launch requires a profile name",
		)
	}

	return nil
}

// Redacted returns the document with its credential removed, for logging and
// for any record that outlives the launch.
func (d LaunchDocument) Redacted() LaunchDocument {
	d.Credential = ""

	return d
}

// Encode serializes the launch document for stdin or a launch file.
func (d LaunchDocument) Encode() ([]byte, error) {
	encoded, err := json.Marshal(d)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode worker launch document")
	}

	return encoded, nil
}

// DecodeLaunchDocument reads one launch document and validates it.
func DecodeLaunchDocument(raw []byte) (LaunchDocument, error) {
	document := LaunchDocument{}
	if err := json.Unmarshal(raw, &document); err != nil {
		return LaunchDocument{}, ctxerrors.Wrap(
			err,
			"decode worker launch document",
		)
	}

	if err := document.Validate(); err != nil {
		return LaunchDocument{}, err
	}

	return document, nil
}

// SessionSocketDirectory is one session's own directory beneath the worker
// socket root.
//
// A Docker worker receives this directory, never the root. The root holds every
// live session's socket, so mounting it would show one worker the path of every
// other session's controller surface. One directory per session is what keeps
// that list private.
func SessionSocketDirectory(socketRoot string, sessionID uuid.UUID) string {
	return filepath.Join(socketRoot, sessionID.String())
}

// SocketPathFor names one session's private socket inside that session's own
// socket directory.
func SocketPathFor(socketRoot string, sessionID uuid.UUID) string {
	return filepath.Join(
		SessionSocketDirectory(socketRoot, sessionID),
		socketFileName,
	)
}

// ValidateSocketRoot refuses a socket root no session socket could fit under.
//
// Every session socket is the root plus a fixed-length name, so the longest
// path this root will ever produce is known before any session exists. Checking
// it at startup turns a deep directory into one clear refusal naming the
// setting to change, instead of a bind failure on the first message a client
// sends.
func ValidateSocketRoot(socketRoot string) error {
	longest := SocketPathFor(socketRoot, uuid.Nil)
	if len(longest) > maxSocketPathBytes {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"worker socket root %q produces a %d byte socket path, "+
				"over the %d byte limit; set PEEN_WORKER_SOCKET_DIR to a "+
				"shorter directory",
			socketRoot,
			len(longest),
			maxSocketPathBytes,
		)
	}

	return nil
}
