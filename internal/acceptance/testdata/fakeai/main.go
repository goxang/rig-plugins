// fakeai stands in for Claude Code under rig's built-in AI: it starts the MCP server rig hands it and
// makes the tool calls in $FAKEAI_SCRIPT instead of a model's, printing stream-json as Claude Code does.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type step struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Expect string         `json:"expect"`
	Fails  bool           `json:"fails"`
}

func main() {
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	for i, a := range os.Args {
		if a == "--mcp-config" && i+1 < len(os.Args) {
			_ = json.Unmarshal([]byte(os.Args[i+1]), &cfg)
		}
	}
	srv := cfg.MCPServers["rig"]
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Env = os.Environ()
	for k, v := range srv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fail(err.Error())
	}
	rd := bufio.NewScanner(out)
	rd.Buffer(make([]byte, 1<<20), 16<<20)
	id := 0
	rpc := func(method string, params any) json.RawMessage {
		id++
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		_, _ = in.Write(append(b, '\n'))
		for rd.Scan() {
			var resp struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct{ Message string }
			}
			if json.Unmarshal(rd.Bytes(), &resp) != nil || resp.ID != id {
				continue
			}
			if resp.Error != nil {
				fail(method + ": " + resp.Error.Message)
			}
			return resp.Result
		}
		fail(method + ": the MCP server closed")
		return nil
	}
	rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "fakeai"}})

	raw, err := os.ReadFile(os.Getenv("FAKEAI_SCRIPT"))
	if err != nil {
		fail(err.Error())
	}
	var steps []step
	if err := json.Unmarshal(raw, &steps); err != nil {
		fail(err.Error())
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "fakeai"})
	for _, s := range steps {
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "mcp__rig__" + s.Tool, "input": s.Args}}}})
		var res struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		_ = json.Unmarshal(rpc("tools/call", map[string]any{"name": s.Tool, "arguments": s.Args}), &res)
		var text []string
		for _, c := range res.Content {
			text = append(text, c.Text)
		}
		got := strings.Join(text, "\n")
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": s.Tool + ": " + got}}}})
		if res.IsError != s.Fails || !strings.Contains(got, s.Expect) {
			fail(fmt.Sprintf("%s %v: error=%v, want %v and %q in:\n%s", s.Tool, s.Args, res.IsError, s.Fails, s.Expect, got))
		}
	}
	_ = in.Close()
	_ = cmd.Wait()
	emit(map[string]any{"type": "result", "subtype": "success", "result": "done"})
}

func emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fail(msg string) {
	emit(map[string]any{"type": "result", "subtype": "error", "is_error": true, "result": msg})
	os.Exit(1)
}
