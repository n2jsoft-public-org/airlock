package mcpserve

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
)

func TestAWSErrorKeepsTheCodeAndMessage(t *testing.T) {
	err := fmt.Errorf("operation error S3: GetObject: %w", &smithy.GenericAPIError{Code: "NoSuchKey", Message: "The specified key does not exist."})
	if got := AWSError(err).Error(); got != "NoSuchKey: The specified key does not exist." {
		t.Fatalf("AWSError = %q", got)
	}
}

func TestAWSErrorWithoutMessageIsTheCode(t *testing.T) {
	if got := AWSError(&smithy.GenericAPIError{Code: "NotFound"}).Error(); got != "NotFound" {
		t.Fatalf("AWSError = %q", got)
	}
}

func TestAWSErrorKeepsTheCredentialsFailure(t *testing.T) {
	credErr := &CredentialsError{Err: errors.New("the SSO session has expired")}
	// The credentials failure itself: errors.Is alone would also accept the SDK's wrapping error, whose
	// message carries the "operation error" prefix.
	got := AWSError(fmt.Errorf("operation error S3: ListBuckets: %w", credErr))
	if !errors.Is(got, credErr) || got.Error() != credErr.Error() {
		t.Fatalf("AWSError = %v", got)
	}
}
