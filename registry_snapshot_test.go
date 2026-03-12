package adaptivemsg

import "testing"

type registrySnapshotMsg struct{}

func (*registrySnapshotMsg) WireName() string {
	return "am.test.RegistrySnapshot"
}

func TestRegistrySnapshotIsolation(t *testing.T) {
	orig := globalRegistry
	globalRegistry = newRegistry()
	t.Cleanup(func() {
		globalRegistry = orig
	})

	snapshot := newRegistrySnapshot()
	wire, err := WireNameOf(&registrySnapshotMsg{})
	if err != nil {
		t.Fatalf("WireNameOf: %v", err)
	}
	if _, ok := snapshot.message(wire); ok {
		t.Fatalf("snapshot should not contain new type")
	}

	if err := RegisterGlobalType[registrySnapshotMsg](); err != nil {
		t.Fatalf("RegisterGlobalType: %v", err)
	}
	if _, ok := snapshot.message(wire); ok {
		t.Fatalf("snapshot should not be mutated by global registration")
	}
	if _, ok := globalRegistry.message(wire); !ok {
		t.Fatalf("global registry missing type")
	}
}
