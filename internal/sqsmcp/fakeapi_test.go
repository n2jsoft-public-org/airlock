package sqsmcp

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// fakeSQS answers with its fields, records the last input of each operation and counts the calls.
type fakeSQS struct {
	err       error
	list      *sqs.ListQueuesInput
	urlIn     *sqs.GetQueueUrlInput
	attrsIn   *sqs.GetQueueAttributesInput
	attrs     map[string]string
	tags      map[string]string
	calls     map[string]int
	queueURL  string
	nextToken string
	urls      []string
	sources   []string
}

func (f *fakeSQS) count(op string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[op]++
}

func (f *fakeSQS) ListQueues(_ context.Context, in *sqs.ListQueuesInput, _ ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error) {
	f.count("ListQueues")
	f.list = in
	out := &sqs.ListQueuesOutput{QueueUrls: f.urls}
	if f.nextToken != "" {
		out.NextToken = &f.nextToken
	}
	return out, f.err
}

func (f *fakeSQS) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	f.count("GetQueueUrl")
	f.urlIn = in
	return &sqs.GetQueueUrlOutput{QueueUrl: &f.queueURL}, f.err
}

func (f *fakeSQS) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	f.count("GetQueueAttributes")
	f.attrsIn = in
	return &sqs.GetQueueAttributesOutput{Attributes: f.attrs}, f.err
}

func (f *fakeSQS) ListQueueTags(_ context.Context, _ *sqs.ListQueueTagsInput, _ ...func(*sqs.Options)) (*sqs.ListQueueTagsOutput, error) {
	f.count("ListQueueTags")
	return &sqs.ListQueueTagsOutput{Tags: f.tags}, f.err
}

func (f *fakeSQS) ListDeadLetterSourceQueues(_ context.Context, _ *sqs.ListDeadLetterSourceQueuesInput, _ ...func(*sqs.Options)) (*sqs.ListDeadLetterSourceQueuesOutput, error) {
	f.count("ListDeadLetterSourceQueues")
	return &sqs.ListDeadLetterSourceQueuesOutput{QueueUrls: f.sources}, f.err
}

func (f *fakeSQS) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.count("ReceiveMessage")
	return &sqs.ReceiveMessageOutput{}, f.err
}

func (f *fakeSQS) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.count("SendMessage")
	return &sqs.SendMessageOutput{}, f.err
}

func (f *fakeSQS) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.count("DeleteMessage")
	return &sqs.DeleteMessageOutput{}, f.err
}
