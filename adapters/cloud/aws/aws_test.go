package aws

import (
	"testing"

	"github.com/goxang/rig/sdk"
)

func TestParseQuery(t *testing.T) {
	m, err := ParseQuery("AWS/EC2 CPUUtilization InstanceId=i-1 p99")
	if err != nil || m.Namespace != "AWS/EC2" || m.Stat != "p99" || m.Dimensions[0] != "InstanceId=i-1" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ParseQuery("AWS/EC2 CPUUtilization bogus"); err == nil {
		t.Fatal("bogus token accepted")
	}
}

func TestParseTail(t *testing.T) {
	l, ok := ParseTail("2026-01-02T15:04:05.123000+00:00 ecs/api/abc GET /health 200")
	if !ok || l.Instance != "ecs/api/abc" || l.Text != "GET /health 200" || l.Time.Year() != 2026 {
		t.Fatalf("%+v %v", l, ok)
	}
}

func TestRows(t *testing.T) {
	f, err := Rows([]byte(`[{"name":"a","memory":128},{"name":"b","memory":null}]`))
	if err != nil || f.Fields[0].Name != "name" || f.Fields[1].Type != sdk.TypeNumber || f.Columns[0][1] != "b" {
		t.Fatalf("%+v %v", f, err)
	}
}
