package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Invoker 把前端的一次 mygo.call 落到真实 Go 方法上。
//
// method 形如 "API.ListAgents"（与 globalThis.mygo.call 的第一个参数完全一致），
// args 是位置参数。实现放在 main 包里（那边才知道 *API），网关只负责路由与 HTTP。
type Invoker func(ctx context.Context, method string, args []json.RawMessage) (any, error)

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// RPCInvoker 用反射在 target（通常是 *API）上按方法名路由。
// 这样做的理由：桌面窗口是靠宿主反射注册方法表的，网页端如果逐个端点手写，
// 必然出现「桌面有、网页没有」的第二份 API 并慢慢漂移。反射复用同一份方法，
// 只暴露导出方法，参数与返回值按 MyGo 的习惯（位置参数 + (值, error)）。
func RPCInvoker(target any) Invoker {
	if target == nil {
		return nil
	}
	typ := reflect.TypeOf(target)
	val := reflect.ValueOf(target)
	var cache sync.Map // 方法名 -> reflect.Method

	return func(ctx context.Context, method string, args []json.RawMessage) (out any, err error) {
		name := strings.TrimSpace(method)
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			return nil, fmt.Errorf("method is empty")
		}

		var m reflect.Method
		if cached, ok := cache.Load(name); ok {
			m = cached.(reflect.Method)
		} else {
			var found bool
			m, found = typ.MethodByName(name)
			if !found {
				return nil, fmt.Errorf("没有这个方法：%s", method)
			}
			// 只放行值接收者/指针接收者上的导出方法；首字母小写的已由 MethodByName 挡掉。
			cache.Store(name, m)
		}

		// 防御：前端传了别的对象名（API.XXX 以外）时也允许，但只在本 target 上解析。
		deferred := func() {
			if r := recover(); r != nil {
				out = nil
				err = fmt.Errorf("%s 执行时 panic：%v", name, r)
			}
		}
		defer deferred()

		nin := m.Type.NumIn() - 1 // 去掉接收者
		callArgs := make([]reflect.Value, 0, nin+1)
		callArgs = append(callArgs, val)

		// ai 是「前端实参」的下标，与形参下标分开：context.Context 由服务端注入，
		// 既不占前端的位置，也不能让后面的参数整体错位（这个错位曾让
		// PromptAgent(ctx, req) 收不到 req）。
		ai := 0
		for i := 0; i < nin; i++ {
			pt := m.Type.In(i + 1)
			if pt.Implements(contextType) {
				if ctx == nil {
					ctx = context.Background()
				}
				callArgs = append(callArgs, reflect.ValueOf(ctx))
				continue
			}
			if m.Type.IsVariadic() && i == nin-1 {
				// 可变参数：剩下的实参都塞进切片。
				rest := args[min(ai, len(args)):]
				slice := reflect.MakeSlice(pt, 0, len(rest))
				for _, raw := range rest {
					v, e := decodeArg(pt.Elem(), raw)
					if e != nil {
						return nil, fmt.Errorf("%s 的第 %d 个参数：%w", name, i+1, e)
					}
					slice = reflect.Append(slice, v)
				}
				callArgs = append(callArgs, slice)
				continue
			}
			var rawArg json.RawMessage
			if ai < len(args) {
				rawArg = args[ai]
			}
			ai++
			v, e := decodeArg(pt, rawArg)
			if e != nil {
				return nil, fmt.Errorf("%s 的第 %d 个参数：%w", name, ai, e)
			}
			callArgs = append(callArgs, v)
		}

		outs := m.Func.Call(callArgs)
		// 末尾是 error 时按 (值, error) 处理；只有 error 时按纯错误返回。
		if len(outs) > 0 {
			last := outs[len(outs)-1]
			if last.Type() == errorType {
				if !last.IsNil() {
					return nil, last.Interface().(error)
				}
				outs = outs[:len(outs)-1]
			}
		}
		if len(outs) == 0 {
			return nil, nil
		}
		return outs[0].Interface(), nil
	}
}

// decodeArg 把一段 JSON 解成目标类型。空 args 或 null 时给零值，
// 保证「前端少传一个参数」不会 panic，而是拿到该类型的零值。
func decodeArg(t reflect.Type, raw json.RawMessage) (reflect.Value, error) {
	ptr := reflect.New(t)
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
			return reflect.Value{}, err
		}
	}
	return ptr.Elem(), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
