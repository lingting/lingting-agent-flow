package pi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/process"
)

// outcomeKind 描述事件消费阶段的结束原因。
type outcomeKind int

// 事件消费阶段的结束原因。
const (
	outcomeSettled outcomeKind = iota
	outcomeCancelled
	outcomeProtocolFailed
	outcomeOutputFailed
)

// String 返回结束原因的名称，用于失败事件的 reason 字段。
func (k outcomeKind) String() string {
	switch k {
	case outcomeSettled:
		return "settled"
	case outcomeCancelled:
		return "cancelled"
	case outcomeProtocolFailed:
		return "protocol_error"
	case outcomeOutputFailed:
		return "output_error"
	default:
		return "unknown"
	}
}

// outcome 是事件消费阶段的结束结果。
type outcome struct {
	kind outcomeKind
	err  error
}

// recordPump 在独立 goroutine 中持续消费 stdout 记录。
//
// 读取必须与等待解耦：调用方可能在等待命令响应，也可能在等待 agent_settled，
// 但 stdout 始终需要被消费，否则子进程会因管道背压停滞。
type recordPump struct {
	records  chan record
	stopCh   chan struct{}
	stopOnce sync.Once
	decoder  *decoder
	mapper   *translator
}

// newRecordPump 启动记录消费协程。
func newRecordPump(
	lines <-chan process.Line,
	dec *decoder,
	mapper *translator,
	buffer int,
) *recordPump {
	if buffer <= 0 {
		buffer = DefaultRecordBuffer
	}
	pump := &recordPump{
		records: make(chan record, buffer),
		stopCh:  make(chan struct{}),
		decoder: dec,
		mapper:  mapper,
	}
	go pump.run(lines)
	return pump
}

// run 读取并解码记录，直到输出结束或停止信号到达。
func (p *recordPump) run(lines <-chan process.Line) {
	defer close(p.records)
	for {
		select {
		case <-p.stopCh:
			// 仍需排空剩余行，避免 stdout 管道背压阻塞子进程退出。
			drain(lines)
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			if !p.deliver(p.decode(line)) {
				drain(lines)
				return
			}
		}
	}
}

// decode 把一行输出解码为记录，读取错误转换为失败记录。
func (p *recordPump) decode(line process.Line) record {
	if line.Err != nil {
		return record{Index: p.decoder.index + 1, Type: recordTypeFailure, Error: errorText(line.Err)}
	}
	rec, err := p.decoder.decode(line.Text)
	if err != nil {
		return record{Index: p.decoder.index, Type: recordTypeFailure, Error: errorText(err)}
	}
	return rec
}

// deliver 投递一条记录，收到停止信号时返回 false。
func (p *recordPump) deliver(rec record) bool {
	select {
	case p.records <- rec:
		return true
	case <-p.stopCh:
		return false
	}
}

// stop 停止消费协程，可重复调用。
func (p *recordPump) stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
}

// awaitResponse 等待指定请求 ID 的响应，期间的事件记录被映射为内部事件。
func (p *recordPump) awaitResponse(
	ctx context.Context,
	id string,
	timeout time.Duration,
) (record, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return record{}, ctx.Err()
		case <-timer.C:
			return record{}, fmt.Errorf("pi: 等待命令 %s 响应超时（%s）", id, timeout)
		case rec, ok := <-p.records:
			if !ok {
				return record{}, fmt.Errorf("pi: 等待命令 %s 响应时输出流已关闭", id)
			}
			if rec.Type == recordTypeFailure {
				return record{}, errors.New(rec.Error)
			}
			if rec.isResponse() {
				if rec.ID != id {
					return record{}, &ProtocolError{
						Record: rec.Index,
						Kind:   kindUnexpectedResponse,
						Detail: fmt.Sprintf("期望请求 %q 的响应，收到 %q", id, rec.ID),
					}
				}
				return rec, nil
			}
			if _, err := p.handle(rec); err != nil {
				return record{}, err
			}
		}
	}
}

// waitSettled 持续消费事件直到 agent_settled、取消或输出流结束。
func (p *recordPump) waitSettled(ctx context.Context) outcome {
	for {
		select {
		case <-ctx.Done():
			return outcome{kind: outcomeCancelled, err: ctx.Err()}
		case rec, ok := <-p.records:
			if !ok {
				return outcome{
					kind: outcomeOutputFailed,
					err:  errors.New("pi: 输出流在 agent_settled 之前关闭"),
				}
			}
			if rec.Type == recordTypeFailure {
				return outcome{kind: outcomeProtocolFailed, err: errors.New(rec.Error)}
			}
			if rec.isResponse() {
				// 请求关联已经失效：没有待匹配的命令却收到响应，属于协议异常。
				return outcome{kind: outcomeProtocolFailed, err: &ProtocolError{
					Record: rec.Index,
					Kind:   kindUnexpectedResponse,
					Detail: fmt.Sprintf("收到未关联请求的响应 %q（命令 %s）", rec.ID, rec.Command),
				}}
			}
			settled, err := p.handle(rec)
			if err != nil {
				return outcome{kind: outcomeProtocolFailed, err: err}
			}
			if settled {
				return outcome{kind: outcomeSettled}
			}
		}
	}
}

// handle 把记录映射为内部事件，并报告是否已收到 agent_settled。
func (p *recordPump) handle(rec record) (bool, error) {
	if p.mapper == nil {
		return false, nil
	}
	if err := p.mapper.handle(rec, time.Now()); err != nil {
		return false, err
	}
	return p.mapper.settled, nil
}

// recordTypeFailure 是记录消费失败时使用的内部标记类型。
const recordTypeFailure = "agentflow.failure"

// drain 排空剩余行，保证 stdout 管道不会因背压阻塞子进程退出。
func drain(lines <-chan process.Line) {
	for range lines {
	}
}
