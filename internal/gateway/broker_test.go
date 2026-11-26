package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestBrokerConcurrentPublishSubscribe 覆盖「N 个发布方持续 Publish +
// M 个订阅方反复 Subscribe/Unsubscribe」的交错场景：取消订阅发生在
// Publish 快照之后时，发布方只能经 done 信号退出，绝不能向已关闭通道发送。
// 用 go test -race 跑，重复 Unsubscribe 必须幂等、不 panic。
func TestBrokerConcurrentPublishSubscribe(t *testing.T) {
	broker := NewBroker()

	const publishers = 4
	const subscribers = 6
	const rounds = 200

	var stop atomic.Bool
	var pubWG sync.WaitGroup

	for i := 0; i < publishers; i++ {
		pubWG.Add(1)
		go func(i int) {
			defer pubWG.Done()
			for !stop.Load() {
				broker.Publish("test.event", map[string]any{"i": i})
			}
		}(i)
	}

	var subWG sync.WaitGroup
	for i := 0; i < subscribers; i++ {
		subWG.Add(1)
		go func() {
			defer subWG.Done()
			for r := 0; r < rounds; r++ {
				id, ch := broker.Subscribe()
				// 非阻塞地消费几条，模拟 SSE 处理器的读取节奏。
				for k := 0; k < 4; k++ {
					select {
					case <-ch:
					default:
					}
				}
				broker.Unsubscribe(id)
				broker.Unsubscribe(id) // 幂等：重复取消不应 panic
			}
		}()
	}

	subWG.Wait()
	stop.Store(true)
	pubWG.Wait()

	if n := broker.Subscribers(); n != 0 {
		t.Fatalf("清理后订阅数应为 0，实际 %d", n)
	}
}
