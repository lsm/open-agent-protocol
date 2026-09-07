package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

var (
	ErrClosed            = errors.New("codex app-server rpc: client closed")
	ErrResponseNotFound  = errors.New("codex app-server rpc: response id is not pending")
	ErrNotificationQueue = errors.New("codex app-server rpc: notification queue is full")
	ErrRequestQueue      = errors.New("codex app-server rpc: reverse request queue is full")
)

type RemoteError struct {
	ID     RequestID
	Object ErrorObject
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("codex app-server rpc error %d for request %s: %s", err.Object.Code, err.ID, err.Object.Message)
}

type IncomingRequest struct {
	ID     RequestID
	Method string
	Params json.RawMessage
	Trace  json.RawMessage
	client *Client
	once   sync.Once
}

func (request *IncomingRequest) Respond(ctx context.Context, result any) error {
	var responseErr error = errors.New("codex app-server rpc: reverse request already resolved")
	request.once.Do(func() {
		data, err := marshalValue(result)
		if err != nil {
			responseErr = err
			return
		}
		responseErr = request.client.write(ctx, Response(request.ID, data))
	})
	return responseErr
}

func (request *IncomingRequest) RespondError(ctx context.Context, code int64, message string, data any) error {
	var responseErr error = errors.New("codex app-server rpc: reverse request already resolved")
	request.once.Do(func() {
		raw, err := marshalOptional(data)
		if err != nil {
			responseErr = err
			return
		}
		responseErr = request.client.write(ctx, ErrorResponse(request.ID, ErrorObject{Code: code, Message: message, Data: raw}))
	})
	return responseErr
}

type NotificationMessage struct {
	Method string
	Params json.RawMessage
}

type callResult struct {
	result json.RawMessage
	err    error
}

type Client struct {
	decoder           *Decoder
	encoder           *Encoder
	closer            io.Closer
	strictResponseIDs bool

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[RequestID]chan callResult
	closed  bool
	err     error

	nextID        atomic.Int64
	requests      chan *IncomingRequest
	notifications chan NotificationMessage
	diagnostics   chan error
	done          chan struct{}
}

type ClientOptions struct {
	FrameLimit        int
	QueueCapacity     int
	FirstRequestID    int64
	CloseReadWriter   io.Closer
	StrictResponseIDs bool
}

func NewClient(reader io.Reader, writer io.Writer, options ClientOptions) *Client {
	capacity := options.QueueCapacity
	if capacity <= 0 {
		capacity = 64
	}
	client := &Client{
		decoder:           NewDecoder(reader, options.FrameLimit),
		encoder:           NewEncoder(writer),
		closer:            options.CloseReadWriter,
		strictResponseIDs: options.StrictResponseIDs,
		pending:           make(map[RequestID]chan callResult),
		requests:          make(chan *IncomingRequest, capacity),
		notifications:     make(chan NotificationMessage, capacity),
		diagnostics:       make(chan error, capacity),
		done:              make(chan struct{}),
	}
	client.nextID.Store(options.FirstRequestID)
	go client.readLoop()
	return client
}

func (client *Client) Requests() <-chan *IncomingRequest         { return client.requests }
func (client *Client) Notifications() <-chan NotificationMessage { return client.notifications }
func (client *Client) Diagnostics() <-chan error                 { return client.diagnostics }
func (client *Client) Done() <-chan struct{}                     { return client.done }

func (client *Client) Err() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.err
}

func (client *Client) Call(ctx context.Context, method string, params any, result any) error {
	if method == "" {
		return ErrInvalidMessage
	}
	id := IntegerID(client.nextID.Add(1))
	paramsJSON, err := marshalOptional(params)
	if err != nil {
		return err
	}
	response := make(chan callResult, 1)
	client.mu.Lock()
	if client.closed {
		err := client.err
		client.mu.Unlock()
		if err != nil {
			return err
		}
		return ErrClosed
	}
	client.pending[id] = response
	client.mu.Unlock()

	if err := client.write(ctx, Request(id, method, paramsJSON)); err != nil {
		client.removePending(id)
		return err
	}
	select {
	case outcome := <-response:
		if outcome.err != nil {
			return outcome.err
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(outcome.result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		client.removePending(id)
		return ctx.Err()
	case <-client.done:
		select {
		case outcome := <-response:
			return outcome.err
		default:
			if err := client.Err(); err != nil {
				return err
			}
			return ErrClosed
		}
	}
}

func (client *Client) Notify(ctx context.Context, method string, params any) error {
	if method == "" {
		return ErrInvalidMessage
	}
	data, err := marshalOptional(params)
	if err != nil {
		return err
	}
	return client.write(ctx, Notification(method, data))
}

func (client *Client) Close() error {
	if client.closer != nil {
		_ = client.closer.Close()
	}
	client.shutdown(ErrClosed)
	return nil
}

func (client *Client) readLoop() {
	for {
		message, err := client.decoder.Decode()
		if err != nil {
			client.shutdown(err)
			return
		}
		switch message.Kind {
		case MessageResponse:
			if !client.deliver(message.ID, callResult{result: cloneRaw(message.Result)}) {
				err := fmt.Errorf("%w: %s", ErrResponseNotFound, message.ID)
				if client.strictResponseIDs {
					client.shutdown(err)
					return
				}
				client.diagnose(err)
			}
		case MessageError:
			if !client.deliver(message.ID, callResult{err: &RemoteError{ID: message.ID, Object: *message.Error}}) {
				err := fmt.Errorf("%w: %s", ErrResponseNotFound, message.ID)
				if client.strictResponseIDs {
					client.shutdown(err)
					return
				}
				client.diagnose(err)
			}
		case MessageRequest:
			incoming := &IncomingRequest{ID: message.ID, Method: message.Method, Params: cloneRaw(message.Params), Trace: cloneRaw(message.Trace), client: client}
			select {
			case client.requests <- incoming:
			default:
				_ = client.write(context.Background(), ErrorResponse(message.ID, ErrorObject{Code: -32603, Message: ErrRequestQueue.Error()}))
				client.shutdown(ErrRequestQueue)
				return
			}
		case MessageNotification:
			notification := NotificationMessage{Method: message.Method, Params: cloneRaw(message.Params)}
			select {
			case client.notifications <- notification:
			default:
				client.shutdown(ErrNotificationQueue)
				return
			}
		}
	}
}

func (client *Client) deliver(id RequestID, outcome callResult) bool {
	client.mu.Lock()
	response, exists := client.pending[id]
	if exists {
		delete(client.pending, id)
	}
	client.mu.Unlock()
	if exists {
		response <- outcome
	}
	return exists
}

func (client *Client) removePending(id RequestID) {
	client.mu.Lock()
	delete(client.pending, id)
	client.mu.Unlock()
}

func (client *Client) diagnose(err error) {
	select {
	case client.diagnostics <- err:
	default:
	}
}

func (client *Client) write(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	client.mu.Lock()
	closed := client.closed
	err := client.err
	client.mu.Unlock()
	if closed {
		if err != nil {
			return err
		}
		return ErrClosed
	}
	return client.encoder.Encode(message)
}

func (client *Client) shutdown(reason error) {
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return
	}
	client.closed = true
	client.err = reason
	pending := client.pending
	client.pending = make(map[RequestID]chan callResult)
	close(client.done)
	client.mu.Unlock()
	for _, response := range pending {
		response <- callResult{err: reason}
	}
}

func marshalValue(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func marshalOptional(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	return marshalValue(value)
}
