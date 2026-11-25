package main

import "testing"

// 网关未启用（gw==nil）时，IPC 接口必须返回空态而不是 panic：
// 切片非 nil（前端拿到 [] 而不是 null），Wired 为空 map。

func TestGatewayStatusNilHost(t *testing.T) {
	api := &API{}
	st := api.GatewayStatus()

	if st.Gateway {
		t.Error("gw==nil 时 Gateway 应为 false")
	}
	if st.Agents == nil {
		t.Error("Agents 切片必须非 nil")
	}
	if st.PendingApprovals == nil {
		t.Error("PendingApprovals 切片必须非 nil")
	}
	if st.Channels == nil {
		t.Error("Channels 切片必须非 nil")
	}
	if st.Wired == nil {
		t.Error("Wired map 必须非 nil")
	}
	if len(st.Agents) != 0 || len(st.PendingApprovals) != 0 || len(st.Channels) != 0 {
		t.Error("gw==nil 时列表应为空")
	}
	if st.Bindings != 0 || st.SSESubscribers != 0 || st.UptimeSec != 0 {
		t.Error("gw==nil 时计数应为 0")
	}
}

func TestGatewayBindingsNilHost(t *testing.T) {
	api := &API{}
	bindings := api.GatewayBindings()
	if bindings == nil {
		t.Error("Bindings 切片必须非 nil")
	}
	if len(bindings) != 0 {
		t.Error("gw==nil 时绑定列表应为空")
	}
}

func TestGatewayDecideApprovalNilHost(t *testing.T) {
	api := &API{}
	if err := api.GatewayDecideApproval("missing", true, false, ""); err == nil {
		t.Error("gw==nil 时决定审批应返回错误")
	}
}
