package main

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func toolOrderBody(items ...string) []byte {
	return []byte(`{"model":"deepseek-v4-flash","input":[` + strings.Join(items, ",") + `]}`)
}

func toolOrderTypes(body []byte) []string {
	types := make([]string, 0)
	for _, item := range gjson.GetBytes(body, "input").Array() {
		types = append(types, item.Get("type").String()+":"+item.Get("call_id").String())
	}
	return types
}

func TestProviderGatewaySerializeToolCallsDisablesParallelCalls(t *testing.T) {
	body := toolOrderBody(`{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"}`)
	got := providerGatewaySerializeToolCalls(body)
	if v := gjson.GetBytes(got, "parallel_tool_calls"); !v.Exists() || v.Bool() {
		t.Fatalf("parallel_tool_calls was not disabled: %s", got)
	}
	// 原有内容不能丢。
	if gjson.GetBytes(got, "input.0.call_id").String() != "call_a" {
		t.Fatalf("input was damaged: %s", got)
	}

	// 已经有该字段时同样被改成 false。
	existing := []byte(`{"model":"m","parallel_tool_calls":true,"input":[]}`)
	if v := gjson.GetBytes(providerGatewaySerializeToolCalls(existing), "parallel_tool_calls"); v.Bool() {
		t.Fatalf("existing parallel_tool_calls=true was not overridden")
	}
}

func TestProviderGatewayToolOrderIsGatedToDeepSeek(t *testing.T) {
	if !providerGatewayRepairsToolCallOrder(&providerGatewaySpec{BaseURL: "https://api.deepseek.com"}) {
		t.Fatal("DeepSeek gateway must repair tool call order")
	}
	for _, baseURL := range []string{
		"https://api.openai.com",
		"https://api.example.com",
		"https://api.deepseek.com.evil.test",
		"",
	} {
		if providerGatewayRepairsToolCallOrder(&providerGatewaySpec{BaseURL: baseURL}) {
			t.Fatalf("gateway %q must not repair tool call order", baseURL)
		}
	}
	if providerGatewayRepairsToolCallOrder(nil) {
		t.Fatal("nil gateway must not repair tool call order")
	}
}

func TestProviderGatewayToolOrderMovesMessageOutOfThePair(t *testing.T) {
	// Codex 的 PostToolUse 钩子会把开发消息写在工具输出之前，
	// DeepSeek 因此判定调用「没有输出」并拒绝整轮。
	body := toolOrderBody(
		`{"type":"message","role":"user","content":"run pwd"}`,
		`{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{}"}`,
		`{"type":"message","role":"developer","content":"hook note"}`,
		`{"type":"function_call_output","call_id":"call_a","output":"/tmp"}`,
	)

	got, relocated, ok := providerGatewayRepairsToolCallOrderBody(body)
	if !ok {
		t.Fatal("reorder should be allowed")
	}
	if relocated != 1 {
		t.Fatalf("relocated = %d, want 1: %s", relocated, got)
	}
	want := []string{
		"message:",
		"function_call:call_a",
		"function_call_output:call_a",
		"message:",
	}
	if strings.Join(toolOrderTypes(got), "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v: %s", toolOrderTypes(got), want, got)
	}
	// 内容不能丢：开发消息仍然在请求里。
	if !strings.Contains(string(got), "hook note") {
		t.Fatalf("hook message was dropped: %s", got)
	}
}

func TestProviderGatewayToolOrderInterleavesParallelCalls(t *testing.T) {
	body := toolOrderBody(
		`{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"}`,
		`{"type":"function_call","call_id":"call_b","name":"read","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call_a","output":"a"}`,
		`{"type":"function_call_output","call_id":"call_b","output":"b"}`,
	)

	got, relocated, ok := providerGatewayRepairsToolCallOrderBody(body)
	if !ok {
		t.Fatal("reorder should be allowed")
	}
	if relocated != 2 {
		t.Fatalf("relocated = %d, want 2: %s", relocated, got)
	}
	want := []string{
		"function_call:call_a",
		"function_call_output:call_a",
		"function_call:call_b",
		"function_call_output:call_b",
	}
	if strings.Join(toolOrderTypes(got), "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v: %s", toolOrderTypes(got), want, got)
	}
	if !gjson.ValidBytes(got) {
		t.Fatalf("result is not valid JSON: %s", got)
	}
}

func TestProviderGatewayToolOrderIsNoOpForOrderedHistory(t *testing.T) {
	body := toolOrderBody(
		`{"type":"message","role":"user","content":"hi"}`,
		`{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call_a","output":"a"}`,
		`{"type":"message","role":"assistant","content":"done"}`,
	)

	got, relocated, ok := providerGatewayRepairsToolCallOrderBody(body)
	if !ok || relocated != 0 {
		t.Fatalf("ordered history must not be touched: relocated=%d ok=%v", relocated, ok)
	}
	if string(got) != string(body) {
		t.Fatalf("ordered history changed: %s", got)
	}
}

func TestProviderGatewayToolOrderLeavesUnpairedAndPrecedingOutputsAlone(t *testing.T) {
	// 没有 call_id 的调用项无法判断归属：放弃重排（保持透传），不能瞎猜。
	mixed := toolOrderBody(
		`{"type":"function_call","name":"read","arguments":"{}"}`,
		`{"type":"message","role":"developer","content":"note"}`,
		`{"type":"function_call_output","output":"a"}`,
	)
	if _, relocated, ok := providerGatewayRepairsToolCallOrderBody(mixed); ok || relocated != 0 {
		t.Fatalf("missing call_id must abort reorder: ok=%v relocated=%d", ok, relocated)
	}

	// 输出排在调用之前属于历史损坏，交给配对修复处理，本函数保持原样。
	preceding := toolOrderBody(
		`{"type":"function_call_output","call_id":"call_a","output":"a"}`,
		`{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"}`,
		`{"type":"message","role":"assistant","content":"done"}`,
	)
	got, relocated, ok := providerGatewayRepairsToolCallOrderBody(preceding)
	if !ok || relocated != 0 {
		t.Fatalf("preceding output scenario: ok=%v relocated=%d", ok, relocated)
	}
	if string(got) != string(preceding) {
		t.Fatalf("preceding output scenario changed the body: %s", got)
	}
}

func TestProviderGatewayToolOrderHandlesCustomToolCalls(t *testing.T) {
	body := toolOrderBody(
		`{"type":"custom_tool_call","call_id":"call_c","name":"apply_patch","input":"patch"}`,
		`{"type":"message","role":"developer","content":"note"}`,
		`{"type":"custom_tool_call_output","call_id":"call_c","output":"ok"}`,
	)

	got, relocated, ok := providerGatewayRepairsToolCallOrderBody(body)
	if !ok || relocated != 1 {
		t.Fatalf("custom tool call pair was not repaired: relocated=%d ok=%v", relocated, ok)
	}
	want := []string{"custom_tool_call:call_c", "custom_tool_call_output:call_c", "message:"}
	if strings.Join(toolOrderTypes(got), "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v: %s", toolOrderTypes(got), want, got)
	}
}
