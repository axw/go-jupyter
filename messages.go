package jupyter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	currentProtocolVersion = "5.0"

	messageTypeKernelInfoRequest = "kernel_info_request"
	messageTypeKernelInfoReply   = "kernel_info_reply"
	messageTypeExecuteRequest    = "execute_request"
	messageTypeExecuteReply      = "execute_reply"
	messageTypeExecuteResult     = "execute_result"
	messageTypeShutdownRequest   = "shutdown_request"
	messageTypeShutdownReply     = "shutdown_reply"
	messageTypeStatus            = "status"

	statusIdle     kernelStatus = "idle"
	statusBusy     kernelStatus = "busy"
	statusStarting kernelStatus = "starting"
)

type message struct {
	Header       messageHeader          `json:"header"`
	ParentHeader messageHeader          `json:"parent_header"`
	Metadata     map[string]interface{} `json:"metadata"`
	Content      json.RawMessage        `json:"content"`
}

type messageHeader struct {
	Id       string `json:"msg_id"`
	Username string `json:"username"`
	Session  string `json:"session"`
	Type     string `json:"msg_type"`
	Version  string `json:"version,omitempty"`
}

type executeRequest struct {
	// Source code to be executed by the kernel, one or more lines.
	Code string `json:"code"`

	// A boolean flag which, if True, signals the kernel to execute
	// this code as quietly as possible.
	// silent=True forces store_history to be False,
	// and will *not*:
	//   - broadcast output on the IOPUB channel
	//   - have an execute_result
	// The default is False.
	Silent bool `json:"silent"`

	// A boolean flag which, if True, signals the kernel to populate history
	// The default is True if silent is False.  If silent is True, store_history
	// is forced to be False.
	StoreHistory bool `json:"store_history"`

	// A dict mapping names to expressions to be evaluated in the
	// user's dict. The rich display-data representation of each will be
	// evaluated after execution.
	// See the display_data content for the structure of the representation data.
	UserExpressions map[string]string `json:"user_expressions"`

	// Some frontends do not support stdin requests.
	// If raw_input is called from code executed from such a frontend,
	// a StdinNotImplementedError will be raised.
	AllowStdin bool `json:"allow_stdin"`

	// A boolean flag, which, if True, does not abort the execution queue,
	// if an exception is encountered.
	// This allows the queued execution of multiple execute_requests, even
	// if they generate exceptions.
	StopOnError bool `json:"stop_on_error"`
}

type executeReply struct {
	Status         string `json:"status"`
	ExecutionCount int    `json:"execution_count"`

	UserExpressions map[string]interface{} `json:"user_expressions,omitempty"`

	ErrorName  string   `json:"ename,omitempty"`
	ErrorValue string   `json:"evalue,omitempty"`
	Traceback  []string `json:"traceback,omitempty"`
}

type executeResult struct {
	ExecutionCount int                    `json:"execution_count"`
	Source         string                 `json:"source"`
	Data           map[string]interface{} `json:"data"`
	Metadata       map[string]interface{} `json:"metadata"`
}

type kernelStatus string

func (s kernelStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ExecutionState string `json:"execution_state"`
	}{string(s)})
}

// deserializeMessage parses a multipart 0MQ message received from a socket
// into a message struct and a slice of identities. parseMessage will verify
// the message signature with the provided key.
//
// See: http://jupyter-client.readthedocs.org/en/latest/messaging.html
func deserializeMessage(parts []string, key []byte) (_ *message, identities []string, _ error) {
	for {
		if len(parts) == 0 {
			return nil, nil, errors.New("message delimiter not found")
		}
		if parts[0] == "<IDS|MSG>" {
			parts = parts[1:]
			break
		}
		identities = append(identities, parts[0])
		parts = parts[1:]
	}

	if len(parts) < 5 {
		return nil, nil, errors.New("not enough parts to message")
	}

	if parts[0] != "" {
		mac := hmac.New(sha256.New, key)
		for _, part := range parts[1:5] {
			mac.Write([]byte(part))
		}
		signature, err := hex.DecodeString(parts[0])
		if err != nil {
			return nil, nil, fmt.Errorf("signature decoding failed: %v", err)
		}
		if !hmac.Equal(mac.Sum(nil), signature) {
			return nil, nil, errors.New("signature validation failed")
		}
	}

	var msg message
	if err := json.Unmarshal([]byte(parts[1]), &msg.Header); err != nil {
		return nil, nil, fmt.Errorf("unmarshalling message header: %v", err)
	}
	if err := json.Unmarshal([]byte(parts[2]), &msg.ParentHeader); err != nil {
		return nil, nil, fmt.Errorf("unmarshalling message parent header: %v", err)
	}
	if err := json.Unmarshal([]byte(parts[3]), &msg.Metadata); err != nil {
		return nil, nil, fmt.Errorf("unmarshalling message metadata: %v", err)
	}
	msg.Content = []byte(parts[4])
	// TODO(axw) verify that content is in serialized-dict format, but
	// leave its deseralisation to individual message type handlers.
	return &msg, identities, nil
}

// newMessage creates a new message with the given type and parent message.
func newMessage(messageType string, parentMessageHeader *messageHeader) (*message, error) {
	messageId, err := newUUID()
	if err != nil {
		return nil, fmt.Errorf("allocating message ID: %v", err)
	}
	msg := &message{
		Header: messageHeader{
			Id:      messageId,
			Type:    messageType,
			Version: currentProtocolVersion,
		},
		Content: json.RawMessage([]byte("{}")),
	}
	if parentMessageHeader != nil {
		msg.ParentHeader = *parentMessageHeader
		msg.Header.Username = parentMessageHeader.Username
		msg.Header.Session = parentMessageHeader.Session
	}
	return msg, nil
}

// serializeMessage converts a message to the Jupyter wire format, signed with
// the given key, ready to be transmitted via 0MQ.
//
// See: http://jupyter-client.readthedocs.org/en/latest/messaging.html
func serializeMessage(msg *message, identities []string, key []byte) ([]string, error) {
	result := make([]string, len(identities), len(identities)+6)
	copy(result, identities)
	result = append(result, "<IDS|MSG>")

	header, err := json.Marshal(msg.Header)
	if err != nil {
		return nil, fmt.Errorf("marshalling message header")
	}
	parentHeader, err := json.Marshal(msg.ParentHeader)
	if err != nil {
		return nil, fmt.Errorf("marshalling message parent header")
	}
	metadata, err := json.Marshal(msg.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshalling message metadata")
	}

	var hmacSignature string
	if len(key) != 0 {
		mac := hmac.New(sha256.New, key)
		mac.Write(header)
		mac.Write(parentHeader)
		mac.Write(metadata)
		mac.Write(msg.Content)
		hmacSignature = hex.EncodeToString(mac.Sum(nil))
	}
	result = append(
		result,
		hmacSignature,
		string(header),
		string(parentHeader),
		string(metadata),
		string(msg.Content),
	)
	return result, nil
}
