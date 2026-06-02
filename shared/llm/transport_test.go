package llm

import (
	"testing"
	"time"
)

func TestSharedPooledTransport_SameTimeoutSharesInstance(t *testing.T) {
	a := sharedPooledTransport(90 * time.Second)
	b := sharedPooledTransport(90 * time.Second)
	if a != b {
		t.Fatal("same timeout should return the same shared transport")
	}
}

func TestSharedPooledTransport_DifferentTimeoutDistinct(t *testing.T) {
	a := sharedPooledTransport(90 * time.Second)
	c := sharedPooledTransport(120 * time.Second)
	if a == c {
		t.Fatal("different timeouts should return distinct transports")
	}
}

func TestSharedPooledTransport_Config(t *testing.T) {
	tr := sharedPooledTransport(30 * time.Second)
	if tr.MaxIdleConnsPerHost != 50 || tr.MaxIdleConns != 200 {
		t.Fatalf("pool sizing wrong: perHost=%d total=%d", tr.MaxIdleConnsPerHost, tr.MaxIdleConns)
	}
	if tr.TLSNextProto == nil {
		t.Fatal("HTTP/2 should be disabled via non-nil empty TLSNextProto")
	}
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("header timeout = %v, want 30s", tr.ResponseHeaderTimeout)
	}
}
