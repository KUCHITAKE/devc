package daemon

import (
	"sort"
	"testing"
)

func TestParseProcNetTCP(t *testing.T) {
	content := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:C350 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 12348 1 0000000000000000 100 0 0 10 0`

	ports := ParseProcNetTCP(content)
	sort.Ints(ports)

	// 0x1F90 = 8080, 0x0016 = 22, 0x0BB8 = 3000
	// Line 3 has state 01 (ESTABLISHED), should be excluded
	expected := []int{22, 3000, 8080}

	if len(ports) != len(expected) {
		t.Fatalf("got %d ports, want %d: %v", len(ports), len(expected), ports)
	}
	for i, p := range ports {
		if p != expected[i] {
			t.Errorf("port[%d] = %d, want %d", i, p, expected[i])
		}
	}
}

func TestParseProcNetTCP_Empty(t *testing.T) {
	ports := ParseProcNetTCP("")
	if len(ports) != 0 {
		t.Errorf("expected empty, got %v", ports)
	}
}

func TestParseProcNetTCP_HeaderOnly(t *testing.T) {
	content := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode`
	ports := ParseProcNetTCP(content)
	if len(ports) != 0 {
		t.Errorf("expected empty, got %v", ports)
	}
}

func TestParseProcNetTCP_DuplicatePorts(t *testing.T) {
	// Same port listening on both IPv4 and IPv6
	content := `  sl  local_address rem_address   st
   0: 00000000:1F90 00000000:0000 0A
   1: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A`

	ports := ParseProcNetTCP(content)
	if len(ports) != 1 {
		t.Errorf("expected 1 deduplicated port, got %v", ports)
	}
	if ports[0] != 8080 {
		t.Errorf("expected port 8080, got %d", ports[0])
	}
}

func TestStaticPortSet(t *testing.T) {
	ports := StaticPortSet([]string{"8080:3000", "5432:5432"})
	if !ports["3000"] {
		t.Error("expected 3000 in set")
	}
	if !ports["5432"] {
		t.Error("expected 5432 in set")
	}
	if ports["8080"] {
		t.Error("8080 is a host port, should not be in set")
	}
}
