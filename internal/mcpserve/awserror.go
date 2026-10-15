package mcpserve

import (
	"errors"
	"fmt"

	"github.com/aws/smithy-go"
)

// AWSError keeps the code and message of an AWS API error, or the credentials failure, without the
// SDK's operation chain.
func AWSError(err error) error {
	if credErr, ok := errors.AsType[*CredentialsError](err); ok {
		return credErr
	}
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		if apiErr.ErrorMessage() == "" { // S3 answers HEAD without a body.
			return errors.New(apiErr.ErrorCode())
		}
		return fmt.Errorf("%s: %s", apiErr.ErrorCode(), apiErr.ErrorMessage())
	}
	return err
}
