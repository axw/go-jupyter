// Package jupyter implements the machinery necessary to implement and run a
// kernel for Jupyter.
package jupyter

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"

	"github.com/davecgh/go-spew/spew"
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
	Shell   *zmq.Socket
	Control *zmq.Socket
	Stdin   *zmq.Socket
	IOPub   *zmq.Socket
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

	var sockets sockets
	var heartbeatSocket *zmq.Socket

	socketPorts := []struct {
		Name   string
		Port   int
		Type   zmq.Type
		Socket **zmq.Socket
	}{
		{"heartbeat", connInfo.HeartbeatPort, zmq.REP, &heartbeatSocket},
		{"shell", connInfo.ShellPort, zmq.ROUTER, &sockets.Shell},
		{"control", connInfo.ControlPort, zmq.ROUTER, &sockets.Control},
		{"stdin", connInfo.StdinPort, zmq.ROUTER, &sockets.Stdin},
		{"iopub", connInfo.IOPubPort, zmq.PUB, &sockets.IOPub},
	}
	for _, socketPort := range socketPorts {
		socket, err := bindSocket(socketPort.Type, socketPort.Port)
		if err != nil {
			// TODO(axw) do we need to close all sockets if one
			// fails? Is terminating the context good enough?
			// TODO(axw) log error if Term fails?
			context.Term()
			return nil, nil, fmt.Errorf(
				"creating %v socket: %v", socketPort.Name, err,
			)
		}
		*socketPort.Socket = socket
	}

	// Message signing key
	//sg.Key = []byte(conn_info.Key)

	go func() {
		err := zmq.Proxy(heartbeatSocket, heartbeatSocket, nil)
		if err != nil {
			log.Printf("error: %v", err)
		}
	}()
	return context, &sockets, nil
}

// RunKernel is the main entry point to start the kernel. This is what is called by the
// kernel executable.
func RunKernel(kernel Kernel, connInfo *ConnectionInfo) error {
	context, sockets, err := createSockets(connInfo)
	if err != nil {
		return err
	}

	k := &kernelRunner{connInfo, sockets, false, kernel}
	err = k.loop()
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
	connInfo *ConnectionInfo
	sockets  *sockets
	shutdown bool
	kernel   Kernel
}

func (k *kernelRunner) loop() error {

	poller := zmq.NewPoller()
	poller.Add(k.sockets.Shell, zmq.POLLIN)
	poller.Add(k.sockets.Stdin, zmq.POLLIN)
	poller.Add(k.sockets.Control, zmq.POLLIN)

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
			case k.sockets.Shell, k.sockets.Control:
				if err := k.handleShellOrControl(msg, ids, polled.Socket); err != nil {
					log.Printf("handling request: %v", err)
				}
			case k.sockets.Stdin:
				if err := k.handleStdin(msg, ids); err != nil {
					log.Printf("handling stdin: %v", err)
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
		// TODO(axw)
		return fmt.Errorf("execute not implemented")
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
		if err := k.reply(messageTypeShutdownReply, &request, msg.Header, ids, socket); err != nil {
			return fmt.Errorf("sending shutdown reply: %v", err)
		}
		k.shutdown = true
		return nil
	default:
		return fmt.Errorf("unknown message type %q", msg.Header.Type)
	}
}

func (k *kernelRunner) handleStdin(msg *message, ids []string) error {
	spew.Dump(msg)
	return fmt.Errorf("stdin not implemented")
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
