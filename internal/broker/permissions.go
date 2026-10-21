// permissions.go records how a run's workers ask Claude Code for permission.
//
// Workers run headless with --print, so nobody is there to answer a prompt.
// The default is bypassPermissions: a worker editing a repository needs its
// tools, and a prompt nobody answers is a worker that cannot work. Some runs
// must not have that. An incident investigation against production is
// started by a watcher before any person has joined, and its workers should
// read and never write. Such a run asks for dontAsk with an allow list:
// Claude Code then runs what the list allows and refuses everything else
// without asking.
//
// It is set when the run is created and kept on disk with it. A daemon
// restart that came back with workers in bypass mode would undo the one
// promise the run was started with.
package broker

import (
	"fmt"
	"slices"
)

// PermissionModes are the modes a run may ask for. Interactive modes are
// left out on purpose: a headless worker in "manual" mode blocks on the
// first prompt forever.
var PermissionModes = []string{"bypassPermissions", "dontAsk"}

// Permissions is the permission policy for every worker of a run. The zero
// value means bypassPermissions.
type Permissions struct {
	Mode string `json:"mode,omitempty"`
	// Allow and Deny are Claude Code permission rules, e.g. "Read" or
	// "Bash(kubectl get:*)". Deny wins over Allow. In dontAsk mode a tool
	// that no rule allows is refused.
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// IsZero reports whether p asks for nothing beyond the default.
func (p Permissions) IsZero() bool {
	return p.Mode == "" && len(p.Allow) == 0 && len(p.Deny) == 0
}

// Equal reports whether p and o are the same policy. An empty mode is
// bypassPermissions, and a nil list is an empty one.
func (p Permissions) Equal(o Permissions) bool {
	return p.mode() == o.mode() && slices.Equal(p.Allow, o.Allow) && slices.Equal(p.Deny, o.Deny)
}

func (p Permissions) mode() string {
	if p.Mode == "" {
		return "bypassPermissions"
	}
	return p.Mode
}

// Validate rejects a policy the launcher could not honour.
func (p Permissions) Validate() error {
	if p.Mode != "" && !slices.Contains(PermissionModes, p.Mode) {
		return fmt.Errorf("permissions.mode %q: must be one of %v", p.Mode, PermissionModes)
	}
	// Rules only narrow anything in dontAsk. In bypass mode an allow list
	// is ignored, and a caller who sent one believed it would hold.
	if p.Mode != "dontAsk" && len(p.Allow) > 0 {
		return fmt.Errorf("permissions.allow needs mode dontAsk; bypassPermissions ignores it")
	}
	return nil
}

// SetPermissions records the run's permission policy.
func (r *Run) SetPermissions(p Permissions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permissions = clonePermissions(p)
}

// Permissions is the run's permission policy.
func (r *Run) Permissions() Permissions {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return clonePermissions(r.permissions)
}

// permissionsPtr is the policy for a snapshot, nil when there is none.
// Called under the run lock.
func (r *Run) permissionsPtr() *Permissions {
	if r.permissions.IsZero() {
		return nil
	}
	p := clonePermissions(r.permissions)
	return &p
}

func clonePermissions(p Permissions) Permissions {
	return Permissions{Mode: p.Mode, Allow: slices.Clone(p.Allow), Deny: slices.Clone(p.Deny)}
}
