// Package orgkit is what the layer plugins (org-management, org-product, ...) share: records kept in
// the plugin's state, reading the units below through the unit's own org, and the exposure cards.
package orgkit

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/sdk"
)

// Kind is a record type: source <Plural> lists them, action <Name> creates, updates (id) or deletes (delete: true) one.
type Kind struct {
	Name, Plural, Title string
	Fields              []Field
	// Derive adds computed columns (declared as Computed fields) to a row before it is listed.
	Derive func(row map[string]any)
	// Sort is a field to order by; a leading - sorts descending. Left out, the latest updated first.
	Sort string
}

type Field struct {
	Name, Type, Description string
	Required, Computed      bool
	Options                 []string
	Default                 any
}

// ponytail: one lock per plugin process; a CLI call and a serve on the same project can still race
var mu sync.Mutex

// Records registers kind's source and action on p.
func Records(p *sdk.Plugin, k Kind) {
	p.Source(k.Plural, func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		rows, err := List(c, k)
		if err != nil {
			return nil, err
		}
		return k.frame(rows), nil
	}, sdk.Describe(first(k.Title, k.Plural)+": "+k.Name+" records"))
	p.Action(k.Name, sdk.Write, func(c *sdk.Ctx, a sdk.Args) (*sdk.Result, error) {
		return Save(c, c.Call.User, k, a)
	}, sdk.Describe("create a "+k.Name+", or update one (id) or delete it (id, delete: true)"), sdk.Arguments(k.schema()))
}

type state interface {
	State(key string) (sdk.StateValue, error)
	SetState(key string, v any) error
}

func load(c state, k Kind) ([]map[string]any, error) {
	sv, err := c.State("records." + k.Plural)
	if err != nil || !sv.Found {
		return nil, err
	}
	b, _ := json.Marshal(sv.Value)
	var rows []map[string]any
	return rows, json.Unmarshal(b, &rows)
}

// List is kind's records, derived and sorted.
func List(c state, k Kind) ([]map[string]any, error) {
	rows, err := load(c, k)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if k.Derive != nil {
			k.Derive(r)
		}
	}
	key, desc := strings.TrimPrefix(k.Sort, "-"), strings.HasPrefix(k.Sort, "-")
	if key == "" {
		key, desc = "updated", true
	}
	slices.SortStableFunc(rows, func(a, b map[string]any) int {
		x := compare(a[key], b[key])
		if desc {
			return -x
		}
		return x
	})
	return rows, nil
}

func compare(a, b any) int {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		return cmp.Compare(fa, fb)
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

// Save creates, updates or deletes one record from a's fields.
func Save(c state, user string, k Kind, a sdk.Args) (*sdk.Result, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := load(c, k)
	if err != nil {
		return nil, err
	}
	id, _ := a["id"].(string)
	i := slices.IndexFunc(rows, func(r map[string]any) bool { return id != "" && r["id"] == id })
	if id != "" && i < 0 {
		return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: "no " + k.Name + " " + id}
	}
	if del, _ := a["delete"].(bool); del || a["delete"] == "true" {
		if i < 0 {
			return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "delete needs an id"}
		}
		rows = slices.Delete(rows, i, i+1)
		return &sdk.Result{Message: k.Name + " " + id + " deleted"}, c.SetState("records."+k.Plural, rows)
	}
	row := map[string]any{}
	if i >= 0 {
		row = rows[i]
	} else {
		row["id"], row["created"], row["by"] = newID(), time.Now().UTC().Format(time.RFC3339), user
	}
	for _, f := range k.Fields {
		v, set := a[f.Name]
		if f.Computed || !set {
			if i < 0 && f.Default != nil {
				row[f.Name] = f.Default
			}
			continue
		}
		if v, err = f.value(v); err != nil {
			return nil, err
		}
		row[f.Name] = v
	}
	for _, f := range k.Fields {
		if f.Required && (row[f.Name] == nil || row[f.Name] == "") {
			return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: k.Name + " needs " + f.Name}
		}
	}
	row["updated"] = time.Now().UTC().Format(time.RFC3339)
	if i < 0 {
		rows = append(rows, row)
	}
	if err := c.SetState("records."+k.Plural, rows); err != nil {
		return nil, err
	}
	return &sdk.Result{Message: k.Name + " " + row["id"].(string) + " saved", Outputs: map[string]any{"id": row["id"]}}, nil
}

// value is v as f's type; the CLI sends strings.
func (f Field) value(v any) (any, error) {
	s, isString := v.(string)
	switch f.Type {
	case "number", "int":
		if isString {
			n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: f.Name + " is a number"}
			}
			return n, nil
		}
		if n, ok := v.(float64); ok {
			return n, nil
		}
		return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: f.Name + " is a number"}
	case "bool":
		if isString {
			return s == "true" || s == "yes", nil
		}
	}
	if len(f.Options) > 0 && !slices.Contains(f.Options, fmt.Sprint(v)) {
		return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: f.Name + " is one of " + strings.Join(f.Options, ", ")}
	}
	return v, nil
}

func (k Kind) schema() sdk.Schema {
	props := map[string]any{
		"id":     map[string]any{"type": "string", "description": "an existing " + k.Name + " to update or delete"},
		"delete": map[string]any{"type": "boolean", "description": "delete the " + k.Name + " id"},
	}
	for _, f := range k.Fields {
		if f.Computed {
			continue
		}
		p := map[string]any{"type": map[string]string{"int": "integer", "bool": "boolean", "time": "string", "": "string"}[f.Type]}
		if p["type"] == nil {
			p["type"] = f.Type
		}
		if f.Description != "" {
			p["description"] = f.Description
		}
		if len(f.Options) > 0 {
			p["enum"] = f.Options
		}
		props[f.Name] = p
	}
	return sdk.Schema{"type": "object", "properties": props}
}

func (k Kind) frame(rows []map[string]any) *sdk.Frame {
	cols := []sdk.Field{sdk.Col("id", sdk.TypeString)}
	for _, f := range k.Fields {
		cols = append(cols, sdk.Col(f.Name, first(map[string]string{"int": sdk.TypeNumber, "number": sdk.TypeNumber, "bool": sdk.TypeBool, "time": sdk.TypeTime}[f.Type], sdk.TypeString)))
	}
	cols = append(cols, sdk.Col("by", sdk.TypeString), sdk.Col("updated", sdk.TypeTime))
	fr := sdk.NewFrame(cols...)
	for _, r := range rows {
		vals := make([]any, len(cols))
		for i, col := range cols {
			vals[i] = r[col.Name]
			if col.Type == sdk.TypeTime {
				vals[i] = when(r[col.Name])
			}
		}
		fr.Append(vals...)
	}
	return fr
}

// when reads a stored time: RFC3339 or a date.
func when(v any) any {
	s, _ := v.(string)
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return nil
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// Rows are a frame's rows.
func Rows(f *sdk.Frame) []map[string]any {
	if f == nil {
		return nil
	}
	b, _ := json.Marshal(f)
	var pf protocol.Frame
	_ = json.Unmarshal(b, &pf)
	return frames.Rows(&pf)
}

type querier interface {
	Query(source string, args sdk.Args) (*sdk.Frame, error)
}

// Children are the units below as this unit's org sees them (org.unit.children).
func Children(c querier) ([]map[string]any, error) {
	f, err := c.Query("org.unit.children", nil)
	return Rows(f), err
}

// Each is source (org.unit.kpis, org.unit.people, ...) of every reachable child, each row tagged with its unit.
func Each(c querier, source string) []map[string]any {
	kids, _ := Children(c)
	per := make([][]map[string]any, len(kids))
	var wg sync.WaitGroup
	for i, k := range kids {
		name, _ := k["name"].(string)
		wg.Go(func() {
			f, err := c.Query(source, sdk.Args{"child": name})
			if err != nil {
				return
			}
			for _, r := range Rows(f) {
				r["unit"] = name
				per[i] = append(per[i], r)
			}
		})
	}
	wg.Wait()
	return slices.Concat(per...)
}

// Health is a scorecard row per unit below: KPIs on target as the value, coloured by the worst sign.
type Health struct {
	Name         string  `json:"name"`
	Title        string  `json:"title"`
	Value        float64 `json:"value"`
	Measure      string  `json:"measure"`
	Status       string  `json:"status"`
	OK           int     `json:"ok"`
	Warn         int     `json:"warn"`
	Err          int     `json:"err"`
	High         int     `json:"high"`
	OpenRequests int     `json:"open_requests"`
	Note         string  `json:"note"`
}

func HealthOf(c querier) ([]Health, error) {
	kids, err := Children(c)
	if err != nil {
		return nil, err
	}
	out := []Health{}
	for _, k := range kids {
		n := func(key string) int { v, _ := k[key].(float64); return int(v) }
		h := Health{Name: Str(k["name"]), Title: first(Str(k["title"]), Str(k["name"])), Measure: "% KPIs on target",
			OK: n("ok"), Warn: n("warn"), Err: n("err"), High: n("high"), OpenRequests: n("open_requests"), Status: "ok"}
		if total := h.OK + h.Warn + h.Err; total > 0 {
			h.Value = float64(100 * h.OK / total)
		}
		reachable, _ := k["reachable"].(bool)
		switch {
		case !reachable || h.Err > 0:
			h.Status = "err"
		case h.Warn > 0 || h.High > 0:
			h.Status = "warn"
		}
		h.Note = fmt.Sprintf("%d ok · %d warn · %d err · %d high items · %d open requests", h.OK, h.Warn, h.Err, h.High, h.OpenRequests)
		if e := Str(k["error"]); e != "" {
			h.Note = e
		}
		out = append(out, h)
	}
	return out, nil
}

// Str reads a string from a row value.
func Str(v any) string { s, _ := v.(string); return s }

// Table is rows as a frame with cols in order; a column's type comes from its first non-empty value.
func Table(rows []map[string]any, cols ...string) *sdk.Frame {
	fields := make([]sdk.Field, len(cols))
	for i, name := range cols {
		typ := sdk.TypeString
		for _, r := range rows {
			if v, ok := r[name]; ok && v != nil {
				switch v.(type) {
				case float64, int:
					typ = sdk.TypeNumber
				case bool:
					typ = sdk.TypeBool
				case time.Time:
					typ = sdk.TypeTime
				case []any, map[string]any:
					typ = sdk.TypeJSON
				}
				break
			}
		}
		fields[i] = sdk.Col(name, typ)
	}
	f := sdk.NewFrame(fields...)
	for _, r := range rows {
		vals := make([]any, len(cols))
		for i, name := range cols {
			vals[i] = r[name]
			if fields[i].Type == sdk.TypeString && r[name] != nil {
				vals[i] = fmt.Sprint(r[name])
			}
		}
		f.Append(vals...)
	}
	return f
}

// Num reads a number from a row value the way records and frames carry it.
func Num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
