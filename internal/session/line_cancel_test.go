package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type startedLineReader struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *startedLineReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(buffer)
}

func TestLineSessionReturnsWhenIdleContextCanceled(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	input := &startedLineReader{Reader: reader, started: make(chan struct{})}
	s := New(Args{In: input, Out: io.Discard})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Run(ctx) }()
	select {
	case <-input.started:
	case <-time.After(time.Second):
		t.Fatal("input read did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bad cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("line input trapped cancellation")
	}
}

type lineCancelModel struct{ cancel context.CancelFunc }

func (m lineCancelModel) Next(ctx context.Context, _ []message.Message) (message.Step, error) {
	m.cancel()
	return message.Step{}, ctx.Err()
}

func TestLineSessionDoesNotReadAgainAfterCanceledModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	s := New(Args{In: reader, Out: io.Discard, Model: lineCancelModel{cancel}, Tools: tools.NewRegistry(nil, tools.Metadata{})})
	result := make(chan error, 1)
	go func() { result <- s.Run(ctx) }()
	if _, err := io.Copy(writer, strings.NewReader("hello\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bad cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read again after model cancellation")
	}
}
