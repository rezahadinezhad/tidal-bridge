package host

import (
	"bytes"
	"net"
	"testing"
)

func TestPortFreeSeesWildcardListeners(t *testing.T) {
	// A dev server listening on [::] (dual stack) must count as busy even
	// though Windows would let 127.0.0.1 be bound on the same port.
	l, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Skip("no IPv6 wildcard listener:", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if PortFree(port) {
		t.Fatalf("port %d reported free while a wildcard listener serves it", port)
	}
	l.Close()
	if !PortFree(port) {
		t.Fatalf("port %d reported busy after its listener closed", port)
	}
}

func TestPathRewriterMapsWorkerPathsAcrossWrites(t *testing.T) {
	const remote = "/data/local/tmp/tidalbridge/state/trees/ws-0123456789abcdef01234567"
	var out bytes.Buffer
	r := newPathRewriter(&out, remote, "R:/Project")
	text := remote + "/src/a.ts\n  1:2 error x\n RUN v4 " + remote + "\n/data/local/other stays\n"
	// Every split point, including inside the path, must give the same result.
	for split := 0; split <= len(text); split++ {
		out.Reset()
		r.Write([]byte(text[:split]))
		r.Write([]byte(text[split:]))
		r.Flush()
		want := "R:/Project/src/a.ts\n  1:2 error x\n RUN v4 R:/Project\n/data/local/other stays\n"
		if out.String() != want {
			t.Fatalf("split %d: got %q", split, out.String())
		}
	}
	// A held-back tail that turns out not to be the path is still written.
	out.Reset()
	r.Write([]byte("ends with /data/lo"))
	r.Flush()
	if out.String() != "ends with /data/lo" {
		t.Fatalf("partial prefix lost: %q", out.String())
	}
}
