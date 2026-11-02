package sqsmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

const (
	// callTimeout bounds each tool call.
	callTimeout = 30 * time.Second
	// listPage is the number of queues list_queues returns per page.
	listPage int32 = 100
)

// sqsAPI is the part of the SQS client the tools use; *sqs.Client implements it.
type sqsAPI interface {
	ListQueues(context.Context, *sqs.ListQueuesInput, ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error)
	GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	ListQueueTags(context.Context, *sqs.ListQueueTagsInput, ...func(*sqs.Options)) (*sqs.ListQueueTagsOutput, error)
	ListDeadLetterSourceQueues(context.Context, *sqs.ListDeadLetterSourceQueuesInput, ...func(*sqs.Options)) (*sqs.ListDeadLetterSourceQueuesOutput, error)
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// queues runs the SQS tool operations.
type queues struct {
	api   sqsAPI
	cache *readCache
}

func newQueues(a sqsAPI) *queues { return &queues{api: a, cache: newReadCache(cacheSize)} }

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

// url is the URL of queue: a queue given as a URL is used as is, a name is resolved.
func (q *queues) url(ctx context.Context, queue string) (string, error) {
	if strings.HasPrefix(queue, "https://") {
		return queue, nil
	}
	out, err := q.api.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(queue)})
	if err != nil {
		return "", mcpserve.AWSError(err)
	}
	return aws.ToString(out.QueueUrl), nil
}

// nameOf is the name of the queue at url: its last path segment.
func nameOf(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}

// redrive is the dead-letter policy of a queue.
type redrive struct {
	DeadLetterTargetARN string `json:"dead_letter_target_arn"`
	MaxReceiveCount     int    `json:"max_receive_count"`
}

// parseRedrive reads a RedrivePolicy; "" is no policy. maxReceiveCount is a number or a string.
func parseRedrive(policy string) (*redrive, error) {
	if policy == "" {
		return nil, nil
	}
	var raw struct {
		DeadLetterTargetARN string          `json:"deadLetterTargetArn"`
		MaxReceiveCount     json.RawMessage `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(policy), &raw); err != nil {
		return nil, fmt.Errorf("invalid redrive policy: %w", err)
	}
	r := &redrive{DeadLetterTargetARN: raw.DeadLetterTargetARN}
	if len(raw.MaxReceiveCount) > 0 {
		n, err := strconv.Atoi(strings.Trim(string(raw.MaxReceiveCount), `"`))
		if err != nil {
			return nil, fmt.Errorf("invalid maxReceiveCount %s in the redrive policy", raw.MaxReceiveCount)
		}
		r.MaxReceiveCount = n
	}
	return r, nil
}

type listQueuesInput struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"only the queues whose name starts with this text"`
	Start  string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
}

type queueSummary struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	FIFO bool   `json:"fifo"`
}

type listQueuesOutput struct {
	Next   string         `json:"next,omitempty"`
	Queues []queueSummary `json:"queues"`
}

func (q *queues) List(ctx context.Context, in *listQueuesInput) (listQueuesOutput, error) {
	out, err := q.api.ListQueues(ctx, &sqs.ListQueuesInput{
		QueueNamePrefix: optional(in.Prefix), MaxResults: aws.Int32(listPage), NextToken: optional(in.Start),
	})
	if err != nil {
		return listQueuesOutput{Queues: []queueSummary{}}, mcpserve.AWSError(err)
	}
	res := listQueuesOutput{Queues: make([]queueSummary, 0, len(out.QueueUrls)), Next: aws.ToString(out.NextToken)}
	for _, u := range out.QueueUrls {
		name := nameOf(u)
		res.Queues = append(res.Queues, queueSummary{Name: name, URL: u, FIFO: strings.HasSuffix(name, ".fifo")})
	}
	return res, nil
}

type queueInput struct {
	Queue string `json:"queue" jsonschema:"queue name or URL"`
}

type queueOutput struct {
	Name                      string            `json:"name"`
	URL                       string            `json:"url"`
	ARN                       string            `json:"arn"`
	KMSKey                    string            `json:"kms_key,omitempty"`
	RedriveAllowPolicy        string            `json:"redrive_allow_policy,omitempty"`
	Policy                    string            `json:"policy,omitempty"`
	Tags                      map[string]string `json:"tags,omitempty"`
	Warnings                  []string          `json:"warnings,omitempty"`
	RedrivePolicy             *redrive          `json:"redrive_policy,omitempty"`
	SourceQueues              []string          `json:"source_queues,omitempty"`
	Visible                   int64             `json:"visible"`
	InFlight                  int64             `json:"in_flight"`
	Delayed                   int64             `json:"delayed"`
	VisibilityTimeout         int               `json:"visibility_timeout_seconds"`
	Retention                 int               `json:"retention_seconds"`
	Delay                     int               `json:"delay_seconds"`
	MaxMessageBytes           int               `json:"max_message_bytes"`
	ReceiveWait               int               `json:"receive_wait_seconds"`
	FIFO                      bool              `json:"fifo"`
	SQSManagedSSE             bool              `json:"sqs_managed_sse"`
	ContentBasedDeduplication bool              `json:"content_based_deduplication"`
}

// intAttr reads a numeric attribute; a missing or invalid one is 0.
func intAttr(attrs map[string]string, name types.QueueAttributeName) int64 {
	n, _ := strconv.ParseInt(attrs[string(name)], 10, 64) //nolint:errcheck // 0 stands for missing.
	return n
}

func (q *queues) Get(ctx context.Context, in *queueInput) (queueOutput, error) {
	u, err := q.url(ctx, in.Queue)
	if err != nil {
		return queueOutput{}, err
	}
	attrs, err := q.api.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(u), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	if err != nil {
		return queueOutput{}, mcpserve.AWSError(err)
	}
	// Tags and source queues are best effort: a role without those permissions still gets the rest.
	var warnings []string
	tags, err := q.api.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(u)})
	if err != nil {
		warnings, tags = append(warnings, "tags: "+mcpserve.AWSError(err).Error()), &sqs.ListQueueTagsOutput{}
	}
	sources, err := q.api.ListDeadLetterSourceQueues(ctx, &sqs.ListDeadLetterSourceQueuesInput{QueueUrl: aws.String(u)})
	if err != nil {
		warnings, sources = append(warnings, "source queues: "+mcpserve.AWSError(err).Error()), &sqs.ListDeadLetterSourceQueuesOutput{}
	}
	a := attrs.Attributes
	policy, err := parseRedrive(a[string(types.QueueAttributeNameRedrivePolicy)])
	if err != nil {
		return queueOutput{}, err
	}
	name := nameOf(u)
	return queueOutput{
		Name: name, URL: u, ARN: a[string(types.QueueAttributeNameQueueArn)], FIFO: strings.HasSuffix(name, ".fifo"),
		ContentBasedDeduplication: a[string(types.QueueAttributeNameContentBasedDeduplication)] == "true",
		Visible:                   intAttr(a, types.QueueAttributeNameApproximateNumberOfMessages),
		InFlight:                  intAttr(a, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible),
		Delayed:                   intAttr(a, types.QueueAttributeNameApproximateNumberOfMessagesDelayed),
		VisibilityTimeout:         int(intAttr(a, types.QueueAttributeNameVisibilityTimeout)),
		Retention:                 int(intAttr(a, types.QueueAttributeNameMessageRetentionPeriod)),
		Delay:                     int(intAttr(a, types.QueueAttributeNameDelaySeconds)),
		MaxMessageBytes:           int(intAttr(a, types.QueueAttributeNameMaximumMessageSize)),
		ReceiveWait:               int(intAttr(a, types.QueueAttributeNameReceiveMessageWaitTimeSeconds)),
		KMSKey:                    a[string(types.QueueAttributeNameKmsMasterKeyId)],
		RedrivePolicy:             policy,
		RedriveAllowPolicy:        a[string(types.QueueAttributeNameRedriveAllowPolicy)],
		Policy:                    a[string(types.QueueAttributeNamePolicy)],
		Tags:                      tags.Tags,
		SourceQueues:              sources.QueueUrls,
		Warnings:                  warnings,
		SQSManagedSSE:             a[string(types.QueueAttributeNameSqsManagedSseEnabled)] == "true",
	}, nil
}
