package transport

import (
	"context"
	"net/http"
)

type Request struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

type Stream struct {
	StatusCode int
	Headers    http.Header
	ID         string
}

type StreamChunk struct {
	Payload []byte
	Error   string
	Done    bool
}

type Host interface {
	Do(context.Context, string, Request) (Response, error)
	OpenStream(context.Context, string, Request) (Stream, error)
	ReadStream(context.Context, string) (StreamChunk, error)
	CloseStream(context.Context, string) error
	Emit(context.Context, string, []byte) error
	CloseOutput(context.Context, string, string)
}
