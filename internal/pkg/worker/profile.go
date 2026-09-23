package worker

import (
	"path/filepath"
	"sort"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Named profiles the plan defines. An operator may define more, but these three
// are the ones the documentation and defaults refer to by name.
const (
	ProfileNative         = "native"
	ProfileDockerSandbox  = "docker.sandbox"
	ProfileDockerHostLike = "docker.host-like"
)

// Mount is one path a Docker worker sees.
//
// Source and Target are literal host paths. Peen does not translate paths: a
// workspace at /srv/work/project is /srv/work/project inside the worker too, so
// paths in prompts, hooks, logs, and tool calls stay truthful. A relocated
// target is a deliberate operator field and is recorded with both paths.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Profile is one operator-defined worker environment.
//
// Every field here comes from deployment configuration. None of it is ever
// taken from model output or a client request: a client names a profile, and
// the operator decides what that name means.
type Profile struct {
	Name string
	Kind Kind

	// Revision increments when an operator changes this profile. Every
	// generation stores the revision it started under, so a durable record
	// says which definition ran.
	Revision int64

	// Image is the container image for a Docker profile, ignored for native.
	// Any reference the Docker daemon can resolve is valid. An image that needs
	// host-account setup must provide Peen's worker entrypoint.
	Image string

	// Mounts are the extra paths beyond the workspace and the global Peen
	// configuration, which the launcher always adds.
	Mounts []Mount

	// AllowDockerSocket mounts the host Docker socket into the worker. That is
	// host-root-equivalent access for the worker, so it is off unless the
	// named profile says otherwise, and it is reported as a capability
	// warning. It is a different grant from the controller's own Docker
	// socket, which is what permits creating worker containers at all.
	AllowDockerSocket bool

	// AllowNetwork leaves the worker's network in place. False asks the
	// launcher for an isolated network.
	AllowNetwork bool

	// AllowPrivilegeEscalation permits passwordless sudo inside the worker.
	AllowPrivilegeEscalation bool
}

// HostRootEquivalent reports whether this profile grants the worker access that
// is effectively host root. A writable Docker socket is the clearest case: any
// process that can reach it can start a privileged container.
func (p Profile) HostRootEquivalent() bool {
	return p.AllowDockerSocket || p.AllowPrivilegeEscalation
}

// CapabilityWarning is the operator-facing warning for a profile that grants
// host-root-equivalent access. It is empty for a profile that does not.
func (p Profile) CapabilityWarning() string {
	if !p.HostRootEquivalent() {
		return ""
	}

	if p.AllowDockerSocket {
		return "this profile mounts the Docker socket into the worker, " +
			"which is host-root-equivalent access"
	}

	return "this profile allows privilege escalation inside the worker"
}

// RequiresDockerAuthority reports whether launching this profile needs the
// controller's own Docker socket. A deployment that cannot reach one refuses
// the profile rather than running the work somewhere else.
func (p Profile) RequiresDockerAuthority() bool {
	return p.Kind == KindDocker
}

// Validate rejects a profile a deployment cannot run.
func (p Profile) Validate() error {
	if err := p.validateIdentity(); err != nil {
		return err
	}

	if err := p.validateImage(); err != nil {
		return err
	}

	if err := p.validateCapabilities(); err != nil {
		return err
	}

	return p.validateMounts()
}

func (p Profile) validateIdentity() error {
	if p.Name == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"execution profile requires a name",
		)
	}

	if !p.Kind.Valid() {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q has an unknown kind",
			p.Name,
		)
	}

	if p.Revision < 0 {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q has a negative revision",
			p.Name,
		)
	}

	return nil
}

func (p Profile) validateImage() error {
	if p.Kind != KindDocker {
		return nil
	}

	// A Docker profile may name no image. The deployment override and the
	// controller's own build version both supply one, and the launcher refuses
	// a launch that ends up with none.
	return nil
}

func (p Profile) validateCapabilities() error {
	if p.Kind != KindNative {
		return nil
	}

	if p.AllowDockerSocket {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q is native and cannot mount a Docker socket",
			p.Name,
		)
	}

	// Privilege escalation is granted by the worker image's entrypoint, which a
	// native worker does not run. Accepting the flag there would promise sudo
	// the deployment cannot give, so the profile is refused instead.
	if p.AllowPrivilegeEscalation {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"execution profile %q is native and cannot grant privilege "+
				"escalation",
			p.Name,
		)
	}

	return nil
}

func (p Profile) validateMounts() error {
	for index, mount := range p.Mounts {
		if !filepath.IsAbs(mount.Source) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"execution profile %q mount %d source is not absolute",
				p.Name,
				index,
			)
		}

		if mount.Target != "" && !filepath.IsAbs(mount.Target) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"execution profile %q mount %d target is not absolute",
				p.Name,
				index,
			)
		}
	}

	return nil
}

// Resolved returns the mounts with every empty target filled in from its
// source. Source equals target is the default, so a profile only writes a
// target when it deliberately relocates a path.
func (p Profile) Resolved() []Mount {
	resolved := make([]Mount, 0, len(p.Mounts))

	for _, mount := range p.Mounts {
		if mount.Target == "" {
			mount.Target = mount.Source
		}

		resolved = append(resolved, mount)
	}

	return resolved
}

// ProfileSet is the deployment's allowed worker profiles.
type ProfileSet struct {
	profiles map[string]Profile
	fallback string
}

// NewProfileSet validates the operator's profiles and the default one.
func NewProfileSet(
	profiles []Profile,
	defaultProfile string,
) (*ProfileSet, error) {
	if len(profiles) == 0 {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"at least one execution profile is required",
		)
	}

	owned := make(map[string]Profile, len(profiles))

	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			return nil, err
		}

		if _, exists := owned[profile.Name]; exists {
			return nil, ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"duplicate execution profile %q",
				profile.Name,
			)
		}

		owned[profile.Name] = profile
	}

	if defaultProfile == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"a default execution profile is required",
		)
	}

	if _, exists := owned[defaultProfile]; !exists {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"default execution profile %q is not defined",
			defaultProfile,
		)
	}

	return &ProfileSet{profiles: owned, fallback: defaultProfile}, nil
}

// Select resolves a requested profile name.
//
// An empty name takes the deployment's default. A name the operator did not
// define is refused with commerr.ErrPermissionDenied, because naming a profile
// is the only worker choice a client gets: it never supplies an image, mount,
// socket, network setting, capability, or credential.
func (s *ProfileSet) Select(name string) (Profile, error) {
	if name == "" {
		name = s.fallback
	}

	profile, found := s.profiles[name]
	if !found {
		return Profile{}, ctxerrors.Wrapf(
			commerr.ErrPermissionDenied,
			"execution profile %q is not allowed by this deployment",
			name,
		)
	}

	return profile, nil
}

// Default returns the deployment's default profile name.
func (s *ProfileSet) Default() string {
	return s.fallback
}

// Names lists every allowed profile name in a stable order.
func (s *ProfileSet) Names() []string {
	names := make([]string, 0, len(s.profiles))
	for name := range s.profiles {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// All returns every allowed profile in a stable order.
func (s *ProfileSet) All() []Profile {
	profiles := make([]Profile, 0, len(s.profiles))
	for _, name := range s.Names() {
		profiles = append(profiles, s.profiles[name])
	}

	return profiles
}
