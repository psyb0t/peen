package docker

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// rootUID is the account a worker may never run as. A worker that ran as root
// would write workspace files the operator cannot manage from the host.
const rootUID = 0

// HostIdentity is the account a Docker worker runs as.
//
// It is the control user's own identity, so files a worker creates in the
// workspace are owned by the same account that owns the workspace on the host.
// There is no fallback to an image-local user or a fixed UID: an incomplete
// identity refuses the launch instead.
type HostIdentity struct {
	Username  string
	GroupName string
	UID       int
	GID       int
	Home      string

	// SupplementaryGIDs are the extra groups the worker needs, such as the
	// Docker socket's group when a host-like profile mounts it.
	SupplementaryGIDs []int
}

// CurrentHostIdentity reads the control process's own account.
//
// The lookup fails when the process runs as a UID with no passwd entry, which
// is what `docker run --user uid:gid` produces against an image that does not
// carry that account. The error names the two variables that resolve it,
// because the underlying message reports only the unknown id.
func CurrentHostIdentity() (HostIdentity, error) {
	current, err := user.Current()
	if err != nil {
		return HostIdentity{}, ctxerrors.Wrapf(
			err,
			"read the controller host identity for uid %d, "+
				"set PEEN_HOST_USERNAME and PEEN_HOST_HOME when the "+
				"controller runs as a UID the image has no account for",
			os.Geteuid(),
		)
	}

	uid, err := strconv.Atoi(current.Uid)
	if err != nil {
		return HostIdentity{}, ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"the controller UID is not numeric",
		)
	}

	gid, err := strconv.Atoi(current.Gid)
	if err != nil {
		return HostIdentity{}, ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"the controller GID is not numeric",
		)
	}

	identity := HostIdentity{
		Username: current.Username,
		UID:      uid,
		GID:      gid,
		Home:     current.HomeDir,
	}

	if group, err := user.LookupGroupId(current.Gid); err == nil {
		identity.GroupName = group.Name
	}

	return identity, nil
}

// CurrentHostIdentityWithOverride returns the controller process identity,
// using an explicit username and home when the process has no passwd entry.
//
// `docker run --user uid:gid` is the correct way to preserve host ownership,
// but the numeric identity commonly does not exist in the image's passwd
// database. A Docker controller supplies PEEN_HOST_USERNAME and PEEN_HOST_HOME
// for that case. They must be supplied together. Native controllers leave both
// empty and keep the operating system lookup as the source of truth.
func CurrentHostIdentityWithOverride(
	username string,
	home string,
) (HostIdentity, error) {
	username = strings.TrimSpace(username)
	home = strings.TrimSpace(home)

	if username == "" && home == "" {
		return CurrentHostIdentity()
	}

	if username == "" || home == "" {
		return HostIdentity{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"Docker worker host username and home must be set together",
		)
	}

	identity := HostIdentity{
		Username: username,
		UID:      os.Geteuid(),
		GID:      os.Getegid(),
		Home:     home,
	}

	groupID := strconv.Itoa(identity.GID)
	if group, err := user.LookupGroupId(groupID); err == nil {
		identity.GroupName = group.Name
	}

	if err := identity.Validate(); err != nil {
		return HostIdentity{}, ctxerrors.Wrap(
			err,
			"validate the Docker worker host identity override",
		)
	}

	return identity, nil
}

// Validate refuses an identity a worker cannot safely run as.
func (i HostIdentity) Validate() error {
	if i.Username == "" {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"worker host identity username",
		)
	}

	if i.UID == rootUID || i.GID == rootUID {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"a Docker worker may not run as root",
		)
	}

	if i.UID < rootUID || i.GID < rootUID {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker host identity UID and GID must not be negative",
		)
	}

	if !filepath.IsAbs(i.Home) {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"worker host identity home must be an absolute path",
		)
	}

	return nil
}

// User renders the identity as the daemon's "uid:gid" user form.
func (i HostIdentity) User() string {
	return strconv.Itoa(i.UID) + ":" + strconv.Itoa(i.GID)
}

// Groups renders the supplementary groups the worker needs.
func (i HostIdentity) Groups() []string {
	groups := make([]string, 0, len(i.SupplementaryGIDs))
	for _, gid := range i.SupplementaryGIDs {
		groups = append(groups, strconv.Itoa(gid))
	}

	return groups
}

// Environment is the identity as the worker's own process sees it.
func (i HostIdentity) Environment() map[string]string {
	return map[string]string{
		"HOME":    i.Home,
		"USER":    i.Username,
		"LOGNAME": i.Username,
	}
}

// BootstrapEnvironment tells the image entrypoint which host account to create
// or reconcile before it drops to it, including the supplementary groups this
// launch resolved. Every Docker worker gets these values because a bare numeric
// UID and GID do not make the host username available inside the image.
func (i HostIdentity) BootstrapEnvironment(groups []string) map[string]string {
	environment := map[string]string{
		bootstrapUIDEnvKey:      strconv.Itoa(i.UID),
		bootstrapGIDEnvKey:      strconv.Itoa(i.GID),
		bootstrapUsernameEnvKey: i.Username,
		bootstrapHomeEnvKey:     i.Home,
	}

	if len(groups) > 0 {
		environment[bootstrapGroupsEnvKey] = strings.Join(groups, ",")
	}

	return environment
}
