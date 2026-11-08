package webview

import "encoding/json"

// 前端与宿主之间的协议。
//
// 形状刻意保持最小：id / method / params 三个字段，回复带 result 或 error。
// 将来插件注册自己的 method 时，只需往同一张分派表里加一条，协议不用动。

// request 是前端发起的一次调用。
type request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// response 是宿主给出的回复。
//
// From 固定为 "host"：底层 WebView2 封装会把前端发出的消息原样回显一次，
// 前端靠这个字段区分"真正的回复"和"自己的回显"。
type response struct {
	From   string `json:"from"`
	ID     int    `json:"id,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func okResponse(id int, result any) response {
	return response{From: "host", ID: id, Result: result}
}

func errResponse(id int, err error) response {
	return response{From: "host", ID: id, Error: err.Error()}
}

// event 是宿主主动推给前端的通知（没有 id，不需要回复）。
//
// 用于"状态被宿主自己改了"的场景：比如用户从托盘菜单切换置顶，
// 界面上的按钮必须跟着变，否则前后端状态会漂移。
// 前端通过 document.addEventListener("host:<event>") 接收。
type event struct {
	From  string `json:"from"`
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}
