package classifier

import (
	"testing"
	"tidalbridge/packages/protocol"
)

func TestClassification(t *testing.T) {
	for _, exe := range []string{"cmd", "pwsh", "thing.exe", "unknown-binary"} {
		s := protocol.JobSpec{Argv: []string{exe}}
		if e := Normalize(&s); e != nil || !s.Requirements.HostOnly {
			t.Fatalf("%s: %+v %v", exe, s, e)
		}
	}
	s := protocol.JobSpec{Argv: []string{"python", "-c", "print(42)"}}
	if e := Normalize(&s); e != nil || s.Requirements.Runtimes["python"] == "" {
		t.Fatal(s, e)
	}
}
func TestUnsafeContracts(t *testing.T) {
	tests := []protocol.JobSpec{{Argv: []string{"python"}, WorkingDirectory: "../personal"}, {Argv: []string{"python"}, ExpectedOutputs: []string{"../../key"}}, {Argv: []string{"python"}, Env: map[string]string{"API_KEY": "test"}}, {Argv: []string{"python"}, Policy: protocol.Policy{Destructive: true, Idempotent: true}}, {Argv: []string{"python"}, Policy: protocol.Policy{Network: "deny"}}}
	for _, s := range tests {
		if e := Normalize(&s); e == nil {
			t.Fatal("expected rejection", s)
		}
	}
}
