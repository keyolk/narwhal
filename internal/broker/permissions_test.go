package broker

import "testing"

func TestPermissionsValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Permissions
		ok   bool
	}{
		{"default", Permissions{}, true},
		{"bypass with deny", Permissions{Mode: "bypassPermissions", Deny: []string{"Bash(rm:*)"}}, true},
		{"dontAsk with allow", Permissions{Mode: "dontAsk", Allow: []string{"Read"}}, true},
		// A headless worker in an interactive mode waits on a prompt
		// nobody will answer.
		{"interactive mode", Permissions{Mode: "manual"}, false},
		// bypassPermissions ignores an allow list; accepting one would
		// let the caller believe it narrows anything.
		{"allow without dontAsk", Permissions{Allow: []string{"Read"}}, false},
	} {
		if err := tc.p.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestPermissionsEqualTreatsDefaultsAsTheSame(t *testing.T) {
	if !(Permissions{}).Equal(Permissions{Mode: "bypassPermissions", Allow: []string{}}) {
		t.Error("an empty policy and an explicit bypass policy should be equal")
	}
	if (Permissions{Mode: "dontAsk"}).Equal(Permissions{}) {
		t.Error("dontAsk and the default must differ")
	}
}

func TestPermissionsSurviveARestore(t *testing.T) {
	// A daemon restart that brought workers back in bypass mode would undo
	// the policy the run was started with.
	r := New().CreateRun("r-1", "investigate", t.TempDir(), "main")
	want := Permissions{Mode: "dontAsk", Allow: []string{"Read", "Bash(kubectl get:*)"}, Deny: []string{"Bash(kubectl delete:*)"}}
	r.SetPermissions(want)
	got := RestoreRun(r.Snapshot()).Permissions()
	if !got.Equal(want) {
		t.Fatalf("restored permissions = %+v, want %+v", got, want)
	}
}

func TestDefaultPermissionsLeaveNoSnapshotField(t *testing.T) {
	if New().CreateRun("r-2", "x", t.TempDir(), "main").Snapshot().Permissions != nil {
		t.Error("a run without a policy should not write one")
	}
}
