package awsapi

import (
	"errors"
	"net"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Unreachable reports whether err is a failure to reach AWS (connection, DNS, timeout, no
// response): such a failure says nothing about the credentials.
func Unreachable(err error) bool {
	// The request never got a response: connection dropped, TLS or proxy failure.
	if _, ok := errors.AsType[*smithyhttp.RequestSendError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	netErr, ok := errors.AsType[net.Error](err)
	return ok && netErr.Timeout()
}
