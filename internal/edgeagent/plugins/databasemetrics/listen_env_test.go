package databasemetrics

import "testing"

func TestReservedPortsFollowExporterListenEnv(t *testing.T) {
	t.Setenv("ONGRID_EDGE_HOSTMETRICS_ADDR", ":19104")
	if owner := reservedListenPorts()["19104"]; owner != "hostmetrics" {
		t.Fatalf("moved hostmetrics port not reserved, owner=%q", owner)
	}
	if _, ok := reservedListenPorts()["9102"]; ok {
		t.Fatal("old hostmetrics port must be released")
	}
}
