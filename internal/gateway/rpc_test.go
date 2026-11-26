package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeAPI struct{}

func (f *fakeAPI) Ping(name string, n int) string             { return name + ":" + itoa(n) }
func (f *fakeAPI) WithCtx(_ context.Context, s string) string { return "ctx:" + s }
func (f *fakeAPI) MaybeFail(fail bool) (string, error) {
	if fail {
		return "", errFake
	}
	return "ok", nil
}
func (f *fakeAPI) OnlyErr(fail bool) error {
	if fail {
		return errFake
	}
	return nil
}
func (f *fakeAPI) unexported() string { return "should not be reachable" }

var errFake = json.Unmarshal([]byte("{"), new(any))

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// 护栏：/api/rpc 是网页端的唯一方法入口，反射一旦错位就是整端不能用。
func TestRPCInvokerPositionalArgs(t *testing.T) {
	in := RPCInvoker(&fakeAPI{})
	got, err := in(context.Background(), "API.Ping", []json.RawMessage{raw("a"), raw(7)})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "a:7" {
		t.Fatalf("got %v", got)
	}
}

// context.Context 必须由服务端注入，而且不占前端的位置参数。
func TestRPCInvokerSkipsContext(t *testing.T) {
	in := RPCInvoker(&fakeAPI{})
	got, err := in(context.Background(), "API.WithCtx", []json.RawMessage{raw("x")})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "ctx:x" {
		t.Fatalf("got %v", got)
	}
}

// (值, error) 的第二个返回值要变成 rpc 错误，不能当成数据发回去。
func TestRPCInvokerPropagatesError(t *testing.T) {
	in := RPCInvoker(&fakeAPI{})
	if _, err := in(context.Background(), "API.MaybeFail", []json.RawMessage{raw(true)}); err == nil {
		t.Fatal("期望拿到错误")
	}
	got, err := in(context.Background(), "API.MaybeFail", []json.RawMessage{raw(false)})
	if err != nil || got != "ok" {
		t.Fatalf("got %v err %v", got, err)
	}
}

// 只有 error 返回值的方法（如 SetXxx 之类）不能把 nil 当第一个返回值发出去。
func TestRPCInvokerOnlyError(t *testing.T) {
	in := RPCInvoker(&fakeAPI{})
	if _, err := in(context.Background(), "API.OnlyErr", []json.RawMessage{raw(true)}); err == nil {
		t.Fatal("期望拿到错误")
	}
	if got, err := in(context.Background(), "API.OnlyErr", []json.RawMessage{raw(false)}); err != nil || got != nil {
		t.Fatalf("got %v err %v", got, err)
	}
}

// 没注册的方法、非导出方法都必须拒绝，不能靠 panic 混过去。
func TestRPCInvokerRejectsUnknown(t *testing.T) {
	in := RPCInvoker(&fakeAPI{})
	for _, name := range []string{"API.Nope", "API.unexported", "Nope"} {
		if _, err := in(context.Background(), name, nil); err == nil {
			t.Fatalf("%s 不该被放行", name)
		}
	}
	if in := RPCInvoker(nil); in != nil {
		t.Fatal("nil target 应返回 nil invoker")
	}
}

// 少传参数时给零值，不要让整个请求因为「前端少写一个字段」而崩。
func TestRPCInvokerMissingArgsUseZeroValue(t *testing.T) {
	in := RPCInvoker(&fakeAPI{})
	got, err := in(context.Background(), "API.Ping", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != ":0" {
		t.Fatalf("got %v", got)
	}
	if _, err := in(context.Background(), "API.Ping", []json.RawMessage{raw("a"), raw("not-an-int")}); err == nil ||
		!strings.Contains(err.Error(), "参数") {
		t.Fatalf("类型不匹配应报参数错误，实际 %v", err)
	}
}
