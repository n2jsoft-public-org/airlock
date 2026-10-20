package awsapi

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// EKSToken returns a bearer token for the EKS cluster, as `aws eks get-token` does: a presigned
// sts:GetCallerIdentity URL carrying the cluster name in a signed header. It takes a pointer
// because aws.Config is large.
func EKSToken(ctx context.Context, cfg *aws.Config, cluster string) (token string, expires time.Time, err error) {
	presigned, err := sts.NewPresignClient(sts.NewFromConfig(*cfg)).PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{},
		func(o *sts.PresignOptions) {
			o.ClientOptions = append(o.ClientOptions, func(so *sts.Options) {
				so.APIOptions = append(so.APIOptions,
					smithyhttp.SetHeaderValue("x-k8s-aws-id", cluster),
					smithyhttp.SetHeaderValue("X-Amz-Expires", "60"))
			})
		})
	if err != nil {
		return "", time.Time{}, err
	}
	// The token is valid for 15 minutes; renew a minute early.
	return "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(presigned.URL)), time.Now().Add(14 * time.Minute), nil
}
