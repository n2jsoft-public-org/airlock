package sqsmcp

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// fakeSQS answers with its fields, records the last input of each operation and counts the calls.
type fakeSQS struct {
	err       error
	list      *sqs.ListQueuesInput
	urlIn     *sqs.GetQueueUrlInput
	attrsIn   *sqs.GetQueueAttributesInput
	recvIn    *sqs.ReceiveMessageInput
	attrs     map[string]string
	tags      map[string]string
	calls     map[string]int
	queueURL  string
	nextToken string
	urls      []string
	sources   []string
	messages  []types.Message
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

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.count("ReceiveMessage")
	f.recvIn = in
	return &sqs.ReceiveMessageOutput{Messages: f.messages}, f.err
}

func (f *fakeSQS) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.count("SendMessage")
	return &sqs.SendMessageOutput{}, f.err
}

func (f *fakeSQS) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.count("DeleteMessage")
	return &sqs.DeleteMessageOutput{}, f.err
}

// fakeSNS answers with its fields, records the last input of each operation and counts the calls.
type fakeSNS struct {
	err       error
	list      *sns.ListTopicsInput
	attrsIn   *sns.GetTopicAttributesInput
	subsIn    *sns.ListSubscriptionsByTopicInput
	subAttrs  *sns.GetSubscriptionAttributesInput
	attrs     map[string]string
	subAttr   map[string]string
	calls     map[string]int
	nextToken string
	subsNext  string
	topics    []string
	subs      []snstypes.Subscription
	tags      []snstypes.Tag
}

func (f *fakeSNS) count(op string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[op]++
}

func (f *fakeSNS) ListTopics(_ context.Context, in *sns.ListTopicsInput, _ ...func(*sns.Options)) (*sns.ListTopicsOutput, error) {
	f.count("ListTopics")
	f.list = in
	out := &sns.ListTopicsOutput{}
	for _, a := range f.topics {
		out.Topics = append(out.Topics, snstypes.Topic{TopicArn: &a})
	}
	if f.nextToken != "" {
		out.NextToken = &f.nextToken
	}
	return out, f.err
}

func (f *fakeSNS) GetTopicAttributes(_ context.Context, in *sns.GetTopicAttributesInput, _ ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	f.count("GetTopicAttributes")
	f.attrsIn = in
	return &sns.GetTopicAttributesOutput{Attributes: f.attrs}, f.err
}

func (f *fakeSNS) ListSubscriptionsByTopic(_ context.Context, in *sns.ListSubscriptionsByTopicInput, _ ...func(*sns.Options)) (*sns.ListSubscriptionsByTopicOutput, error) {
	f.count("ListSubscriptionsByTopic")
	f.subsIn = in
	out := &sns.ListSubscriptionsByTopicOutput{Subscriptions: f.subs}
	if f.subsNext != "" {
		out.NextToken = &f.subsNext
	}
	return out, f.err
}

func (f *fakeSNS) GetSubscriptionAttributes(_ context.Context, in *sns.GetSubscriptionAttributesInput, _ ...func(*sns.Options)) (*sns.GetSubscriptionAttributesOutput, error) {
	f.count("GetSubscriptionAttributes")
	f.subAttrs = in
	return &sns.GetSubscriptionAttributesOutput{Attributes: f.subAttr}, f.err
}

func (f *fakeSNS) ListTagsForResource(context.Context, *sns.ListTagsForResourceInput, ...func(*sns.Options)) (*sns.ListTagsForResourceOutput, error) {
	f.count("ListTagsForResource")
	return &sns.ListTagsForResourceOutput{Tags: f.tags}, f.err
}
