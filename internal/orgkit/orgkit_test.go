package orgkit

import (
	"encoding/json"
	"testing"

	"github.com/goxang/rig/sdk"
)

type memState map[string]any

func (m memState) State(key string) (sdk.StateValue, error) {
	v, ok := m[key]
	return sdk.StateValue{Found: ok, Value: v}, nil
}

func (m memState) SetState(key string, v any) error {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	m[key] = out
	return nil
}

var initiative = Kind{Name: "initiative", Plural: "initiatives", Sort: "-score",
	Fields: []Field{
		{Name: "title", Required: true},
		{Name: "stage", Options: []string{"idea", "next", "now", "done"}, Default: "idea"},
		{Name: "reach", Type: "number"}, {Name: "effort", Type: "number"},
		{Name: "score", Type: "number", Computed: true},
	},
	Derive: func(r map[string]any) {
		reach, _ := r["reach"].(float64)
		effort, _ := r["effort"].(float64)
		if effort > 0 {
			r["score"] = reach / effort
		}
	},
}

func TestRecords(t *testing.T) {
	st := memState{}
	if _, err := Save(st, "ana", initiative, sdk.Args{"stage": "now"}); err == nil {
		t.Error("a record without its required title saved")
	}
	if _, err := Save(st, "ana", initiative, sdk.Args{"title": "x", "stage": "someday"}); err == nil {
		t.Error("a stage outside its options saved")
	}
	a, err := Save(st, "ana", initiative, sdk.Args{"title": "QR payments", "reach": "900", "effort": "3"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Save(st, "ana", initiative, sdk.Args{"title": "Bill split", "reach": 100.0, "effort": 2.0, "score": 1e9})
	rows, _ := List(st, initiative)
	if len(rows) != 2 || rows[0]["title"] != "QR payments" || rows[0]["score"] != 300.0 || rows[0]["stage"] != "idea" || rows[0]["by"] != "ana" {
		t.Fatalf("rows = %v", rows)
	}
	id := a.Outputs["id"].(string)
	if _, err := Save(st, "bo", initiative, sdk.Args{"id": id, "stage": "now"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = List(st, initiative)
	if rows[0]["stage"] != "now" || rows[0]["title"] != "QR payments" {
		t.Errorf("updated = %v", rows[0])
	}
	if f := initiative.frame(rows); len(Rows(f)) != 2 {
		t.Errorf("frame rows = %v", Rows(f))
	}
	if _, err := Save(st, "bo", initiative, sdk.Args{"id": id, "delete": "true"}); err != nil {
		t.Fatal(err)
	}
	if rows, _ = List(st, initiative); len(rows) != 1 {
		t.Errorf("after delete = %v", rows)
	}
}

type fakeOrg map[string][]map[string]any

func (f fakeOrg) Query(source string, args sdk.Args) (*sdk.Frame, error) {
	key := source
	if c, _ := args["child"].(string); c != "" {
		key += ":" + c
	}
	fr := sdk.NewFrame(sdk.Col("name", sdk.TypeString), sdk.Col("kind", sdk.TypeString), sdk.Col("widget", sdk.TypeString),
		sdk.Col("unit", sdk.TypeString), sdk.Col("inputs", sdk.TypeJSON), sdk.Col("reachable", sdk.TypeBool), sdk.Col("value", sdk.TypeNumber))
	for _, r := range f[key] {
		fr.Append(r["name"], r["kind"], r["widget"], r["unit"], r["inputs"], r["reachable"], r["value"])
	}
	return fr, nil
}

func TestEachAndCards(t *testing.T) {
	org := fakeOrg{
		"org.unit.children":    {{"name": "switch", "reachable": true}, {"name": "wallet", "reachable": true}},
		"org.unit.kpis:switch": {{"name": "uptime", "value": 99.9}},
		"org.unit.kpis:wallet": {{"name": "uptime", "value": 99.1}},
		"org.unit.exposed:*": {
			{"name": "payments-flow", "kind": "source", "widget": "graph", "unit": "switch"},
			{"name": "restart", "kind": "action", "unit": "switch", "inputs": []any{map[string]any{"name": "reason", "required": true}}},
		},
	}
	if rows := Each(org, "org.unit.kpis"); len(rows) != 2 || rows[0]["unit"] == rows[1]["unit"] {
		t.Errorf("each = %v", rows)
	}
	n, err := Cards(org, "*", nil)
	if err != nil || n.Widget != "grid" || len(n.Children) != 2 {
		t.Fatalf("cards = %+v, %v", n, err)
	}
	g := n.Children[0]
	if g.Widget != "graph" || g.Bind.Source != "org.unit.show" || g.Bind.Args["child"] != "switch" {
		t.Errorf("graph card = %+v", g)
	}
	form := n.Children[1].Children[0]
	schema := form.Props["schema"].(map[string]any)
	if form.Widget != "form" || form.Props["action"] != "org.unit.invoke" || len(schema["required"].([]any)) != 1 {
		t.Errorf("form card = %+v", form)
	}
}
