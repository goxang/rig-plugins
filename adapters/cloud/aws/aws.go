// Package aws drives the aws CLI with the user's profiles and SSO: CloudWatch as metrics/1 and
// logs/1, and resource browsers for EC2, ECS, EKS, RDS, S3 and Lambda.
package aws

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/sh"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/sdk"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindMetrics, "aws", "Amazon CloudWatch metrics (Namespace Metric Dim=Value Stat)", New)
	plugin.Register(core.KindLogs, "aws", "Amazon CloudWatch Logs", New)
}

type Options struct {
	Profile string `yaml:"profile"`
	Region  string `yaml:"region"`
	// LogGroup is a service's log group; {service} is replaced by its name.
	LogGroup string `yaml:"log_group"`
}

type AWS struct{ opt Options }

func New(_ core.Env, c *spec.Component) (any, error) {
	a := &AWS{}
	if err := c.Decode(&a.opt); err != nil {
		return nil, err
	}
	if a.opt.LogGroup == "" {
		a.opt.LogGroup = "{service}"
	}
	return a, nil
}

func (a *AWS) cli(args ...string) *sh.Cmd {
	return CLI(a.opt.Profile, a.opt.Region, args...)
}

// CLI is the aws command with a profile and region when set, and JSON output.
func CLI(profile, region string, args ...string) *sh.Cmd {
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	return sh.New("aws", append(args, "--output", "json")...)
}

// MetricQuery is a CloudWatch metric as rig queries it: "AWS/EC2 CPUUtilization InstanceId=i-1 Average".
type MetricQuery struct {
	Namespace, Metric string
	Dimensions        []string
	Stat              string
}

var stats = []string{"Average", "Sum", "Minimum", "Maximum", "SampleCount"}

func ParseQuery(q string) (MetricQuery, error) {
	f := strings.Fields(q)
	if len(f) < 2 {
		return MetricQuery{}, fmt.Errorf("a CloudWatch query is `Namespace Metric [Dim=Value ...] [Stat]`, got %q", q)
	}
	m := MetricQuery{Namespace: f[0], Metric: f[1], Stat: "Average"}
	for _, t := range f[2:] {
		switch {
		case strings.Contains(t, "="):
			m.Dimensions = append(m.Dimensions, t)
		case slices.Contains(stats, t) || len(t) > 1 && t[0] == 'p':
			m.Stat = t
		default:
			return MetricQuery{}, fmt.Errorf("%q is neither Dim=Value nor a statistic", t)
		}
	}
	return m, nil
}

func (m MetricQuery) labels() map[string]string {
	l := map[string]string{"__name__": m.Metric, "namespace": m.Namespace}
	for _, d := range m.Dimensions {
		k, v, _ := strings.Cut(d, "=")
		l[k] = v
	}
	return l
}

func (a *AWS) points(ctx context.Context, m MetricQuery, start, end time.Time, step time.Duration) ([]core.Point, error) {
	period := max(60, int(step.Round(time.Minute).Seconds()))
	args := []string{"cloudwatch", "get-metric-statistics", "--namespace", m.Namespace, "--metric-name", m.Metric,
		"--start-time", start.UTC().Format(time.RFC3339), "--end-time", end.UTC().Format(time.RFC3339), "--period", strconv.Itoa(period)}
	if slices.Contains(stats, m.Stat) {
		args = append(args, "--statistics", m.Stat)
	} else {
		args = append(args, "--extended-statistics", m.Stat)
	}
	if len(m.Dimensions) > 0 {
		args = append(args, "--dimensions")
		for _, d := range m.Dimensions {
			k, v, _ := strings.Cut(d, "=")
			args = append(args, "Name="+k+",Value="+v)
		}
	}
	raw, err := a.cli(args...).Output(ctx)
	if err != nil {
		return nil, err
	}
	var res struct {
		Datapoints []map[string]any
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	var out []core.Point
	for _, d := range res.Datapoints {
		t, _ := time.Parse(time.RFC3339, fmt.Sprint(d["Timestamp"]))
		v, ok := d[m.Stat].(float64)
		if ext, isExt := d["ExtendedStatistics"].(map[string]any); isExt {
			v, ok = ext[m.Stat].(float64)
		}
		if ok {
			out = append(out, core.Point{T: t, V: v})
		}
	}
	slices.SortFunc(out, func(a, b core.Point) int { return a.T.Compare(b.T) })
	return out, nil
}

func (a *AWS) Instant(ctx context.Context, query string) ([]core.Sample, error) {
	m, err := ParseQuery(query)
	if err != nil {
		return nil, err
	}
	pts, err := a.points(ctx, m, time.Now().Add(-10*time.Minute), time.Now(), time.Minute)
	if err != nil || len(pts) == 0 {
		return nil, err
	}
	return []core.Sample{{Labels: m.labels(), Value: pts[len(pts)-1].V}}, nil
}

func (a *AWS) Range(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]core.Series, error) {
	m, err := ParseQuery(query)
	if err != nil {
		return nil, err
	}
	pts, err := a.points(ctx, m, start, end, step)
	if err != nil {
		return nil, err
	}
	return []core.Series{{Labels: m.labels(), Points: pts}}, nil
}

// ParseTail reads a `aws logs tail --format detailed` line: time, stream, message.
func ParseTail(l string) (core.LogLine, bool) {
	f := strings.SplitN(l, " ", 3)
	if len(f) < 3 {
		return core.LogLine{}, false
	}
	t, err := time.Parse("2006-01-02T15:04:05.999999-07:00", f[0])
	if err != nil {
		return core.LogLine{}, false
	}
	return core.LogLine{Time: t, Instance: f[1], Text: f[2]}, true
}

// ponytail: one aws logs tail per service; CloudWatch Logs Insights would merge them server side.
func (a *AWS) Logs(ctx context.Context, q core.LogQuery) (<-chan core.LogLine, error) {
	since := q.Since
	if since == 0 {
		since = 15 * time.Minute
	}
	out := make(chan core.LogLine, 512)
	done := make(chan struct{}, len(q.Services))
	for _, svc := range q.Services {
		args := []string{"logs", "tail", strings.ReplaceAll(a.opt.LogGroup, "{service}", svc), "--format", "detailed", "--since", since.String()}
		if q.Follow {
			args = append(args, "--follow")
		}
		if q.Match != "" && !q.Regex {
			args = append(args, "--filter-pattern", strconv.Quote(q.Match))
		}
		x := a.cli(args...).Exec(ctx)
		stdout, err := x.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := x.Start(); err != nil {
			return nil, fmt.Errorf("aws: %s", sh.WithHint(err.Error()))
		}
		go func() {
			defer func() { _ = x.Wait(); done <- struct{}{} }()
			sc := bufio.NewScanner(stdout)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				l, ok := ParseTail(sc.Text())
				if !ok || q.Instance != "" && l.Instance != q.Instance {
					continue
				}
				l.Service = svc
				select {
				case out <- l:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		for range q.Services {
			<-done
		}
		close(out)
	}()
	return out, nil
}

// browsers are the resource sources: the CLI call and the JMESPath that flattens it into rows.
var browsers = map[string]struct {
	desc  string
	args  []string
	query string
}{
	"aws.ec2": {"EC2 instances", []string{"ec2", "describe-instances"},
		"Reservations[].Instances[].{id:InstanceId,name:(Tags[?Key=='Name'].Value)[0],state:State.Name,type:InstanceType,private_ip:PrivateIpAddress,public_ip:PublicIpAddress,launched:LaunchTime}"},
	"aws.ecs": {"ECS clusters", []string{"ecs", "describe-clusters", "--clusters"},
		"clusters[].{name:clusterName,status:status,services:activeServicesCount,running:runningTasksCount,pending:pendingTasksCount}"},
	"aws.eks": {"EKS clusters", []string{"eks", "list-clusters"}, "clusters[].{name:@}"},
	"aws.rds": {"RDS instances", []string{"rds", "describe-db-instances"},
		"DBInstances[].{id:DBInstanceIdentifier,engine:Engine,version:EngineVersion,class:DBInstanceClass,status:DBInstanceStatus,endpoint:Endpoint.Address}"},
	"aws.s3":     {"S3 buckets", []string{"s3api", "list-buckets"}, "Buckets[].{name:Name,created:CreationDate}"},
	"aws.lambda": {"Lambda functions", []string{"lambda", "list-functions"}, "Functions[].{name:FunctionName,runtime:Runtime,memory:MemorySize,timeout:Timeout,modified:LastModified}"},
}

// Register serves the resource browsers, in plugins.aws's profile and region.
func Register(p *sdk.Plugin) {
	for name, b := range browsers {
		p.Source(name, func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
			args := append(slices.Clone(b.args), "--query", b.query)
			if name == "aws.ecs" {
				clusters, err := ecsClusters(c)
				if err != nil {
					return nil, err
				}
				if len(clusters) == 0 {
					return Rows([]byte("[]"))
				}
				args = append(slices.Clone(b.args), clusters...)
				args = append(args, "--query", b.query)
			}
			raw, err := CLI(forge.Config(c, "profile"), forge.Config(c, "region"), args...).Output(c)
			if err != nil {
				return nil, err
			}
			return Rows(raw)
		}, sdk.Describe(b.desc), sdk.Cache("30s"))
	}
}

func ecsClusters(c *sdk.Ctx) ([]string, error) {
	raw, err := CLI(forge.Config(c, "profile"), forge.Config(c, "region"), "ecs", "list-clusters", "--query", "clusterArns").Output(c)
	if err != nil {
		return nil, err
	}
	var arns []string
	return arns, json.Unmarshal(raw, &arns)
}

// Rows is a JSON array of flat objects as a frame, columns in first-seen order.
func Rows(raw []byte) (*sdk.Frame, error) {
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	var cols []string
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// key order from the first object, which map decoding loses
	if tok, _ := dec.Token(); tok == json.Delim('[') {
		if tok, _ := dec.Token(); tok == json.Delim('{') {
			for dec.More() {
				k, _ := dec.Token()
				cols = append(cols, fmt.Sprint(k))
				var skip json.RawMessage
				_ = dec.Decode(&skip)
			}
		}
	}
	f := &sdk.Frame{}
	for _, c := range cols {
		col := make([]any, len(rows))
		typ := ""
		for i, r := range rows {
			col[i] = r[c]
			t := sdk.TypeJSON
			switch r[c].(type) {
			case nil:
				continue
			case string:
				t = sdk.TypeString
			case float64:
				t = sdk.TypeNumber
			}
			if typ != "" && typ != t {
				t = sdk.TypeJSON
			}
			typ = t
		}
		f.Fields = append(f.Fields, sdk.Field{Name: c, Type: cmp.Or(typ, sdk.TypeString)})
		f.Columns = append(f.Columns, col)
	}
	return f, nil
}
