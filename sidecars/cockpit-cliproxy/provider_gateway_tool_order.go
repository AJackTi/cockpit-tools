package main

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 严格 Responses 上游（DeepSeek）按「位置」校验工具调用：每个 tool call 的下一个项必须是它
// 自己的输出，否则整轮请求被拒（`No tool output found for tool call ...`），而且被拒的这一轮
// 会留在客户端历史里，线程此后无法继续，只能卸载再加载。
//
// Codex 在正常路径上就会破坏这个顺序：工具执行完的 PostToolUse 钩子会立刻把开发消息写进历史，
// 可能早于工具输出项落盘，于是历史里出现
//
//	function_call -> message -> function_call_output
//
// 官方上游只按 `call_id` 配对、不校验位置，所以一直没暴露；DeepSeek 会校验。
//
// 这里在请求出口把顺序还原：每个调用后面紧跟自己的输出，其余项保持相对顺序。已经在位的项不动，
// 因此正常历史（官方账号、以及本来就合法的 DeepSeek 历史）是逐字节 no-op。

const (
	providerToolOrderCallType       = "function_call"
	providerToolOrderCustomCallType = "custom_tool_call"
	providerToolOrderOutputType     = "function_call_output"
	providerToolOrderCustomOutput   = "custom_tool_call_output"
)

// providerGatewayRepairsToolCallOrder 报告该上游是否要求「输出紧跟调用」。
//
// 只对 DeepSeek 官方上游启用：官方上游不校验位置，不需要为它重排历史（重排只会白白改变
// 提示词前缀、影响缓存命中），其它第三方上游也未观察到该要求。
func providerGatewayRepairsToolCallOrder(gateway *providerGatewaySpec) bool {
	return isDeepSeekResponsesGateway(gatewayBaseURL(gateway))
}

func gatewayBaseURL(gateway *providerGatewaySpec) string {
	if gateway == nil {
		return ""
	}
	return gateway.BaseURL
}

func providerToolOrderIsCallItem(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case providerToolOrderCallType, providerToolOrderCustomCallType, "tool_call", "mcp_tool_call":
		return true
	default:
		return false
	}
}

func providerToolOrderIsOutputItem(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case providerToolOrderOutputType, providerToolOrderCustomOutput, "tool_call_output", "mcp_tool_call_output":
		return true
	default:
		return false
	}
}

// providerGatewayRepairsToolCallOrderBody 还原「输出紧跟调用」的顺序。
//
// 返回重建后的请求体与被搬动的输出项数量；`ok` 为 false 表示当前请求无法安全重排
// （例如调用项缺少 `call_id`），调用方应当放弃重排、保持原样转发。
func providerGatewayRepairsToolCallOrderBody(body []byte) ([]byte, int, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, 0, true
	}
	items := input.Array()
	if len(items) < 3 {
		// 少于三项不可能出现「调用与输出之间夹了东西」。
		return body, 0, true
	}

	callIndexByID := make(map[string]int)
	outputIndexesByID := make(map[string][]int)
	for index, item := range items {
		itemType := item.Get("type").String()
		callID := strings.TrimSpace(item.Get("call_id").String())
		switch {
		case providerToolOrderIsCallItem(itemType):
			if callID == "" {
				// 没有 call_id 就没法判断归属，交给配对修复处理，这里不猜。
				return body, 0, false
			}
			if _, seen := callIndexByID[callID]; !seen {
				callIndexByID[callID] = index
			}
		case providerToolOrderIsOutputItem(itemType):
			if callID == "" {
				return body, 0, false
			}
			outputIndexesByID[callID] = append(outputIndexesByID[callID], index)
		}
	}
	if len(outputIndexesByID) == 0 {
		return body, 0, true
	}

	rebuilt := make([]string, 0, len(items))
	emitted := make([]bool, len(items))
	relocated := 0
	changed := false
	for index, item := range items {
		if emitted[index] {
			continue
		}
		itemType := item.Get("type").String()
		if providerToolOrderIsCallItem(itemType) {
			callID := strings.TrimSpace(item.Get("call_id").String())
			outputs := outputIndexesByID[callID]
			if len(outputs) > 0 {
				// 只有排在调用之后的输出才可能被搬过来；排在调用之前的属于历史损坏，
				// 不在本函数职责内（配对修复负责丢弃），保持原位让上游给出明确错误。
				relocatable := make([]int, 0, len(outputs))
				for _, outputIndex := range outputs {
					if outputIndex > index {
						relocatable = append(relocatable, outputIndex)
					}
				}
				alreadyOrdered := len(relocatable) == len(outputs)
				if alreadyOrdered {
					for offset, outputIndex := range relocatable {
						if outputIndex != index+1+offset {
							alreadyOrdered = false
							break
						}
					}
				}
				if len(relocatable) > 0 && !alreadyOrdered {
					changed = true
					relocated += len(relocatable)
					emitted[index] = true
					rebuilt = append(rebuilt, item.Raw)
					for _, outputIndex := range relocatable {
						emitted[outputIndex] = true
						rebuilt = append(rebuilt, items[outputIndex].Raw)
					}
					continue
				}
			}
		}
		emitted[index] = true
		rebuilt = append(rebuilt, item.Raw)
	}
	if !changed {
		return body, 0, true
	}

	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return body, 0, false
	}
	if !providerToolOrderPairsAreAdjacent(gjson.GetBytes(updated, "input")) {
		// 重排后仍不满足相邻（例如同一调用有多个输出），放弃改动，避免把请求改坏。
		return body, 0, false
	}
	return updated, relocated, true
}

// providerToolOrderPairsAreAdjacent 校验每个调用后面紧跟自己的输出。
func providerToolOrderPairsAreAdjacent(input gjson.Result) bool {
	if !input.IsArray() {
		return true
	}
	items := input.Array()
	for index, item := range items {
		if !providerToolOrderIsCallItem(item.Get("type").String()) {
			continue
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			continue
		}
		if index+1 >= len(items) {
			// 本请求里没有该调用的输出（可能尚未落盘），无法判断。
			return true
		}
		next := items[index+1]
		if !providerToolOrderIsOutputItem(next.Get("type").String()) {
			return false
		}
		if strings.TrimSpace(next.Get("call_id").String()) != callID {
			return false
		}
	}
	return true
}

// providerGatewayToolOrderDiagnostic 供请求诊断日志使用。
func providerGatewayToolOrderDiagnostic(relocated int) string {
	return fmt.Sprintf("relocated %d displaced tool output item(s)", relocated)
}

// providerGatewaySerializeToolCalls 关闭上游的并行工具调用。
//
// 顺序与配对问题都源自并行批次：Codex 的 app-server 可能在下一次采样请求里抢先带上还没落盘的
// 调用，形成「有 call 无 output」或错位的历史。让上游一次只返回一个调用可以从源头避开这个竞态，
// 顺序还原与配对补齐则作为兜底处理已经落盘的历史。
func providerGatewaySerializeToolCalls(body []byte) []byte {
	updated, err := sjson.SetBytes(body, "parallel_tool_calls", false)
	if err != nil {
		return body
	}
	return updated
}
