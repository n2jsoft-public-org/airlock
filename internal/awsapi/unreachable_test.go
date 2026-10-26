package awsapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestUnreachable(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"op error":                 {refused, true},
		"dns error":                {&net.DNSError{Err: "no such host", Name: "sts.eu-west-3.amazonaws.com"}, true},
		"timeout":                  {timeoutError{}, true},
		"sdk chain":                {fmt.Errorf("operation error STS: GetCallerIdentity: %w", &url.Error{Op: "Post", URL: "https://sts", Err: refused}), true},
		"closed before a response": {&smithyhttp.RequestSendError{Err: &url.Error{Op: "Post", URL: "https://sts", Err: io.EOF}}, true},
		"plain error":              {errors.New("ExpiredToken: the security token is expired"), false},
		"nil":                      {nil, false},
	} {
		if got := awsapi.Unreachable(tc.err); got != tc.want {
			t.Errorf("%s: Unreachable = %v, want %v", name, got, tc.want)
		}
	}
}

func TestUnreachableConnectionDroppedBeforeAResponse(t *testing.T) {
	awsapitest.Env(t, awsapitest.DropConnection(t), "dev")
	cfg, err := awsapi.NewConfigs().Get(context.Background(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	if err == nil || !awsapi.Unreachable(err) {
		t.Fatalf("err = %v, want AWS out of reach", err)
	}
}
