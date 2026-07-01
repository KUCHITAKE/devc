package preflight

import (
	"reflect"
	"testing"
)

func TestAnalyze_NoRunningWorkspaces(t *testing.T) {
	r := Analyze("app", "/home/me/app", []string{"3000", "5173:5173"}, nil)
	if len(r.PortConflicts) != 0 {
		t.Fatalf("expected no port conflicts, got %+v", r.PortConflicts)
	}
	if r.HasBlocking() {
		t.Fatal("expected no blocking conflict")
	}
}

func TestAnalyze_ExplicitPortConflictBlocks(t *testing.T) {
	running := []RunningWS{{Name: "app", Path: "/other/app", HostPorts: []int{3000}}}
	r := Analyze("app", "/home/me/app", []string{"3000:3000"}, running)

	if len(r.PortConflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", r.PortConflicts)
	}
	c := r.PortConflicts[0]
	if c.HostPort != 3000 || !c.Explicit || c.Holder.Path != "/other/app" {
		t.Fatalf("unexpected conflict: %+v", c)
	}
	if !r.HasBlocking() {
		t.Fatal("explicit port conflict should block")
	}
}

func TestAnalyze_BarePortConflictIsAdvisory(t *testing.T) {
	running := []RunningWS{{Name: "app", Path: "/other/app", HostPorts: []int{3000}}}
	r := Analyze("app", "/home/me/app", []string{"3000"}, running)

	if len(r.PortConflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", r.PortConflicts)
	}
	if r.PortConflicts[0].Explicit {
		t.Fatal("bare port should not be explicit")
	}
	if r.HasBlocking() {
		t.Fatal("bare port conflict must not block (it auto-remaps)")
	}
}

func TestAnalyze_MappedHostPort(t *testing.T) {
	// 8080:80 pins host port 8080, not 80.
	running := []RunningWS{{Name: "web", Path: "/other/web", HostPorts: []int{8080}}}
	r := Analyze("app", "/home/me/app", []string{"8080:80"}, running)
	if len(r.PortConflicts) != 1 || r.PortConflicts[0].HostPort != 8080 {
		t.Fatalf("expected conflict on host 8080, got %+v", r.PortConflicts)
	}
}

func TestAnalyze_IPHostContainerSpec(t *testing.T) {
	running := []RunningWS{{Name: "web", Path: "/other/web", HostPorts: []int{3000}}}
	r := Analyze("app", "/home/me/app", []string{"127.0.0.1:3000:3000"}, running)
	if len(r.PortConflicts) != 1 || !r.PortConflicts[0].Explicit {
		t.Fatalf("expected explicit conflict on host 3000, got %+v", r.PortConflicts)
	}
}

func TestAnalyze_SelfIsExcluded(t *testing.T) {
	// A running workspace at the same path is the current one restarting.
	running := []RunningWS{{Name: "app", Path: "/home/me/app", HostPorts: []int{3000}}}
	r := Analyze("app", "/home/me/app", []string{"3000:3000"}, running)
	if len(r.PortConflicts) != 0 {
		t.Fatalf("self should not conflict, got %+v", r.PortConflicts)
	}
	if len(r.SameName) != 0 {
		t.Fatalf("self should not be a same-name match, got %+v", r.SameName)
	}
}

func TestAnalyze_SameNameNotice(t *testing.T) {
	running := []RunningWS{{Name: "app", Path: "/other/app", HostPorts: []int{9999}}}
	r := Analyze("app", "/home/me/app", []string{"3000"}, running)
	if !reflect.DeepEqual(r.SameName, running) {
		t.Fatalf("expected same-name notice for %+v, got %+v", running, r.SameName)
	}
}

func TestAnalyze_DifferentNameNoNotice(t *testing.T) {
	running := []RunningWS{{Name: "other", Path: "/other/other", HostPorts: []int{9999}}}
	r := Analyze("app", "/home/me/app", []string{"3000"}, running)
	if len(r.SameName) != 0 {
		t.Fatalf("different name should not notice, got %+v", r.SameName)
	}
}

func TestAnalyze_DuplicateDesiredPortReportedOnce(t *testing.T) {
	running := []RunningWS{{Name: "app", Path: "/other/app", HostPorts: []int{3000}}}
	r := Analyze("app", "/home/me/app", []string{"3000", "3000:3000"}, running)
	if len(r.PortConflicts) != 1 {
		t.Fatalf("expected a single conflict for host 3000, got %+v", r.PortConflicts)
	}
}
