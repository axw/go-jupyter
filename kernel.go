// Package jupyter implements the machinery necessary to implement and run a
// kernel for Jupyter.
package jupyter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"runtime"

	zmq "github.com/pebbe/zmq4"
)

// ConnectionInfo stores the contents of the kernel connection file created by
// Jupyter.
type ConnectionInfo struct {
	Key             string `json:"key"`
	IP              string `json:"ip"`
	Transport       string `json:"transport"`
	SignatureScheme string `json:"signature_scheme"`

	StdinPort     int `json:"stdin_port"`
	ControlPort   int `json:"control_port"`
	IOPubPort     int `json:"iopub_port"`
	HeartbeatPort int `json:"hb_port"`
	ShellPort     int `json:"shell_port"`
}

// ReadConnectionFile reads the contents of the connection file at the
// specified path.
func ReadConnectionFile(connectionFilePath string) (*ConnectionInfo, error) {
	var connInfo ConnectionInfo
	data, err := ioutil.ReadFile(connectionFilePath)
	if err != nil {
		return nil, fmt.Errorf("reading connection file: %v", err)
	}
	if err := json.Unmarshal(data, &connInfo); err != nil {
		return nil, fmt.Errorf("unmarshalling connection file: %v", err)
	}
	return &connInfo, nil
}

// sockets holds the sockets for communicating with Jupyter.
type sockets struct {
	Heartbeat socket
	Shell     socket
	Control   socket
	Stdin     socket
	IOPub     socket
}

func (s *sockets) sockets() []*socket {
	return []*socket{
		&s.Heartbeat,
		&s.Shell,
		&s.Control,
		&s.Stdin,
		&s.IOPub,
	}
}

func (s *sockets) tryClose() {
	for _, socketPtr := range s.sockets() {
		if socketPtr.Socket == nil {
			continue
		}
		err := socketPtr.Socket.Close()
		if err != nil {
			log.Printf("error closing %v socket: %v", socketPtr.Name, err)
		}
	}
}

type socket struct {
	*zmq.Socket
	Name string
	Port int
	Type zmq.Type
}

// createSockets sets up the 0MQ sockets through which the kernel will
// communicate.
func createSockets(connInfo *ConnectionInfo) (*zmq.Context, *sockets, error) {
	context, err := zmq.NewContext()
	if err != nil {
		return nil, nil, err
	}

	bindSocket := func(t zmq.Type, port int) (*zmq.Socket, error) {
		addr := fmt.Sprintf(
			"%s://%s:%v", connInfo.Transport, connInfo.IP, port,
		)
		socket, err := context.NewSocket(t)
		if err != nil {
			return nil, err
		}
		if err := socket.Bind(addr); err != nil {
			socket.Close()
			return nil, err
		}
		return socket, nil
	}

	sockets := sockets{
		Heartbeat: socket{Name: "heartbeat", Port: connInfo.HeartbeatPort, Type: zmq.REP},
		Shell:     socket{Name: "shell", Port: connInfo.ShellPort, Type: zmq.ROUTER},
		Control:   socket{Name: "control", Port: connInfo.ControlPort, Type: zmq.ROUTER},
		Stdin:     socket{Name: "stdin", Port: connInfo.StdinPort, Type: zmq.ROUTER},
		IOPub:     socket{Name: "iopub", Port: connInfo.IOPubPort, Type: zmq.PUB},
	}

	for _, socketPtr := range sockets.sockets() {
		socket, err := bindSocket(socketPtr.Type, socketPtr.Port)
		if err == nil {
			socketPtr.Socket = socket
			err = socket.SetLinger(0)
		}
		if err != nil {
			sockets.tryClose()
			if err := context.Term(); err != nil {
				log.Printf("error terminating context: %v", err)
			}
			return nil, nil, fmt.Errorf(
				"creating %v socket: %v", socketPtr.Name, err,
			)
		}
	}

	go zmq.Proxy(sockets.Heartbeat.Socket, sockets.Heartbeat.Socket, nil)
	return context, &sockets, nil
}

// RunKernel is the main entry point to start the kernel. This is what is called by the
// kernel executable.
func RunKernel(kernel Kernel, connInfo *ConnectionInfo) error {
	context, sockets, err := createSockets(connInfo)
	if err != nil {
		return err
	}
	log.Println("created sockets")

	k := &kernelRunner{
		connInfo:         connInfo,
		sockets:          sockets,
		kernel:           kernel,
		shutdown:         false,
		executionCounter: 0,
	}
	err = k.loop()
	sockets.tryClose()
	err2 := context.Term()
	if err == nil {
		err = err2
	} else if err2 != nil {
		log.Printf("error terminating: %v", err2)
	}
	return err
}

// kernelRunner handles the communication between Jupyter and the provided
// Kernel.
type kernelRunner struct {
	connInfo         *ConnectionInfo
	sockets          *sockets
	kernel           Kernel
	shutdown         bool
	executionCounter int
}

func (k *kernelRunner) loop() error {

	poller := zmq.NewPoller()
	poller.Add(k.sockets.Shell.Socket, zmq.POLLIN)
	poller.Add(k.sockets.Stdin.Socket, zmq.POLLIN)
	poller.Add(k.sockets.Control.Socket, zmq.POLLIN)

	for !k.shutdown {
		polled, err := poller.Poll(-1)
		if err != nil {
			return fmt.Errorf("poll failed: %v", err)
		}
		for _, polled := range polled {
			msg, ids, err := k.readMessage(polled.Socket)
			if err != nil {
				return fmt.Errorf("reading message: %v", err)
			}
			switch polled.Socket {
			case k.sockets.Shell.Socket, k.sockets.Control.Socket:
				err := k.handleShellOrControl(msg, ids, polled.Socket)
				if err != nil {
					log.Printf("error handling request: %v", err)
				}
			case k.sockets.Stdin.Socket:
				if err := k.handleStdin(msg, ids); err != nil {
					log.Printf("error handling stdin: %v", err)
				}
			}
			if k.shutdown {
				break
			}
		}
	}
	return nil
}

func (k *kernelRunner) readMessage(socket *zmq.Socket) (*message, []string, error) {
	parts, err := socket.RecvMessage(0)
	if err != nil {
		return nil, nil, err
	}
	return deserializeMessage(parts, []byte(k.connInfo.Key))
}

func (k *kernelRunner) handleShellOrControl(msg *message, ids []string, socket *zmq.Socket) error {
	switch msg.Header.Type {
	case messageTypeKernelInfoRequest:
		info := k.kernel.Info()
		info.ProtocolVersion = currentProtocolVersion
		return k.reply(messageTypeKernelInfoReply, info, msg.Header, ids, socket)
	case messageTypeExecuteRequest:
		return k.handleExecuteRequest(msg, ids, socket)
	case messageTypeShutdownRequest:
		var request struct {
			Restart bool `json:"restart"`
		}
		if err := json.Unmarshal(msg.Content, &request); err != nil {
			return fmt.Errorf("unmarshalling shutdown request")
		}
		if err := k.kernel.Shutdown(request.Restart); err != nil {
			return fmt.Errorf("shutting down: %v", err)
		}
		if err := k.reply(
			messageTypeShutdownReply, &request, msg.Header, ids, socket,
		); err != nil {
			return fmt.Errorf("sending shutdown reply: %v", err)
		}
		k.shutdown = true
		return nil
	case messageTypeIsCompleteRequest:
		return k.handleIsCompleteRequest(msg, ids, socket)
	case messageTypeCompleteRequest:
		return k.handleCompleteRequest(msg, ids, socket)
	default:
		return fmt.Errorf("unknown message type %q", msg.Header.Type)
	}
}

func (k *kernelRunner) handleExecuteRequest(msg *message, ids []string, socket *zmq.Socket) error {
	var request executeRequest
	if err := json.Unmarshal(msg.Content, &request); err != nil {
		return fmt.Errorf("unmarshalling execute request")
	}
	if request.StoreHistory {
		k.executionCounter++
	}

	reply := executeReply{
		Status:         "ok",
		ExecutionCount: k.executionCounter,
	}
	results, traceback, err := k.execute(request)
	if err != nil {
		reply.Status = "error"
		reply.ErrorName = fmt.Sprintf("%T", err)
		reply.ErrorValue = err.Error()
		reply.Traceback = traceback
		errorContent := executeError{
			ExecutionCount: reply.ExecutionCount,
			ErrorName:      reply.ErrorName,
			ErrorValue:     reply.ErrorValue,
			Traceback:      reply.Traceback,
		}
		if err := k.publish(
			messageTypeError, &errorContent, msg.Header,
		); err != nil {
			return fmt.Errorf("sending execution error: %v", err)
		}
	}

	if reply.Status == "ok" {
		for _, result := range results {
			resultData, resultMetadata, err := renderDisplayData(result)
			if err != nil {
				return fmt.Errorf("rendering execution result: %v", err)
			}
			resultContent := executeResult{
				Source:         "kernel",
				ExecutionCount: k.executionCounter,
				Data:           resultData,
				Metadata:       resultMetadata,
			}
			if err := k.publish(
				messageTypeExecuteResult, &resultContent, msg.Header,
			); err != nil {
				return fmt.Errorf("sending execution result: %v", err)
			}
		}
		// TODO(axw) evaluate user expressions
		reply.UserExpressions = make(map[string]interface{})
	}

	if err := k.reply(messageTypeExecuteReply, &reply, msg.Header, ids, socket); err != nil {
		return fmt.Errorf("sending execute reply: %v", err)
	}

	// The kernel implicitly goes into the "busy" state while handling an
	// execution request; the kernel must explicitly return to the "idle"
	// state before the frontend will send new reqeusts.
	return k.publish(messageTypeStatus, statusIdle, msg.Header)
}

func (k *kernelRunner) execute(request executeRequest) (
	results []interface{},
	traceback []string,
	err error,
) {
	traceback, err = withRecovery(func() error {
		results, err = k.kernel.Execute(request.Code, ExecuteOptions{
			Silent:       request.Silent,
			StoreHistory: request.StoreHistory,
		})
		return err
	})
	return results, traceback, err
}

// handleIsCompleteRequest handles "is_complete_request" messages,
// used to inform the frontend whether or not a code block is
// complete.
func (k *kernelRunner) handleIsCompleteRequest(
	msg *message, ids []string, socket *zmq.Socket,
) error {
	var request isCompleteRequest
	if err := json.Unmarshal(msg.Content, &request); err != nil {
		return fmt.Errorf("unmarshalling is-complete request")
	}
	var reply isCompleteReply
	// TODO(axw) withRecovery
	switch completeness := k.kernel.Completeness(request.Code); completeness {
	case Complete, Incomplete, InvalidCode:
		reply.Status = string(completeness)
	default:
		reply.Status = string(UnknownCompleteness)
	}
	return k.reply(
		messageTypeIsCompleteReply, &reply, msg.Header, ids, socket,
	)
}

// handleCompleteRequest handles "complete_request" messages,
// to implement code-completion.
func (k *kernelRunner) handleCompleteRequest(
	msg *message, ids []string, socket *zmq.Socket,
) error {
	var request completeRequest
	if err := json.Unmarshal(msg.Content, &request); err != nil {
		return fmt.Errorf("unmarshalling completion request")
	}
	var result *CompletionResult
	var err error
	traceback, err := withRecovery(func() error {
		result, err = k.kernel.Complete(request.Code, request.CursorPos)
		if err == nil {
			if result.Matches == nil {
				result.Matches = []string{}
			}
			if result.Metadata == nil {
				result.Metadata = make(map[string]interface{})
			}
		}
		return err
	})
	if err != nil {
		reply := completeReply{
			Status:     "error",
			ErrorName:  fmt.Sprintf("%T", err),
			ErrorValue: err.Error(),
			Traceback:  traceback,
		}
		if err := k.publish(
			messageTypeCompleteReply, &reply, msg.Header,
		); err != nil {
			return fmt.Errorf("sending completion error: %v", err)
		}
		return nil
	}
	reply := completeReply{
		Matches:     result.Matches,
		CursorStart: result.CursorStart,
		CursorEnd:   result.CursorEnd,
		Metadata:    map[string]interface{}{},
		Status:      "ok",
	}
	return k.reply(
		messageTypeCompleteReply, &reply, msg.Header, ids, socket,
	)
}

func (k *kernelRunner) handleStdin(msg *message, ids []string) error {
	return fmt.Errorf("stdin not implemented")
}

// withRecovery calls the provided function and a traceback if an error
// or panic occurs.
func withRecovery(f func() error) (traceback []string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			buf := make([]byte, 8192)
			buf = buf[:runtime.Stack(buf, false)]
			scanner := bufio.NewScanner(bytes.NewBuffer(buf))
			for scanner.Scan() {
				traceback = append(traceback, scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				panic(err) // should not happen with a bytes.Buffer
			}

			switch recovered := recovered.(type) {
			case error:
				err = recovered
			default:
				err = fmt.Errorf("%s", recovered)
			}
		}
	}()
	if err = f(); err != nil {
		traceback = []string{err.Error()}
	}
	return traceback, err
}

// publish sends a message on the IOPub socket.
func (k *kernelRunner) publish(
	messageType string, content interface{},
	requestHeader messageHeader,
) error {
	return k.reply(messageType, content, requestHeader, []string{messageType}, k.sockets.IOPub.Socket)
}

// reply sends a reply message with the specified message type and content,
// using the supplied parent message header, IDs, and socket.
func (k *kernelRunner) reply(
	messageType string, content interface{},
	requestHeader messageHeader, ids []string,
	socket *zmq.Socket,
) error {
	msg, err := newMessage(messageType, &requestHeader)
	if err != nil {
		return fmt.Errorf("constructing reply message: %v")
	}
	marshalledContent, err := json.Marshal(content)
	msg.Content = marshalledContent
	return k.sendMessage(msg, ids, socket)
}

func (k *kernelRunner) sendMessage(msg *message, ids []string, socket *zmq.Socket) error {
	parts, err := serializeMessage(msg, ids, []byte(k.connInfo.Key))
	if err != nil {
		return fmt.Errorf("serializing message: %v", err)
	}
	if _, err := socket.SendMessage(parts); err != nil {
		return fmt.Errorf("sending message: %v", err)
	}
	return nil
}
