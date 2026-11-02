package sqsmcp

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// pendingARN is the SubscriptionArn of a subscription not yet confirmed.
const pendingARN = "PendingConfirmation"

// snsAPI is the part of the SNS client the tools use; *sns.Client implements it.
type snsAPI interface {
	ListTopics(context.Context, *sns.ListTopicsInput, ...func(*sns.Options)) (*sns.ListTopicsOutput, error)
	GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
	ListSubscriptionsByTopic(context.Context, *sns.ListSubscriptionsByTopicInput, ...func(*sns.Options)) (*sns.ListSubscriptionsByTopicOutput, error)
	GetSubscriptionAttributes(context.Context, *sns.GetSubscriptionAttributesInput, ...func(*sns.Options)) (*sns.GetSubscriptionAttributesOutput, error)
	ListTagsForResource(context.Context, *sns.ListTagsForResourceInput, ...func(*sns.Options)) (*sns.ListTagsForResourceOutput, error)
}

// topics runs the SNS tool operations.
type topics struct {
	api snsAPI
}

func newTopics(a snsAPI) *topics { return &topics{api: a} }

// topicName is the name of the topic with arn: its last ":" segment.
func topicName(arn string) string {
	return arn[strings.LastIndex(arn, ":")+1:]
}

// rest is the attributes without the named ones, which the output already carries.
func rest(attrs map[string]string, known ...string) map[string]string {
	out := maps.Clone(attrs)
	for _, k := range known {
		delete(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type listTopicsInput struct {
	Start string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
}

type topicSummary struct {
	ARN  string `json:"arn"`
	Name string `json:"name"`
}

type listTopicsOutput struct {
	Next   string         `json:"next,omitempty"`
	Topics []topicSummary `json:"topics"`
}

func (t *topics) List(ctx context.Context, in *listTopicsInput) (listTopicsOutput, error) {
	out, err := t.api.ListTopics(ctx, &sns.ListTopicsInput{NextToken: optional(in.Start)})
	if err != nil {
		return listTopicsOutput{Topics: []topicSummary{}}, mcpserve.AWSError(err)
	}
	res := listTopicsOutput{Topics: make([]topicSummary, 0, len(out.Topics)), Next: aws.ToString(out.NextToken)}
	for _, tp := range out.Topics {
		arn := aws.ToString(tp.TopicArn)
		res.Topics = append(res.Topics, topicSummary{ARN: arn, Name: topicName(arn)})
	}
	return res, nil
}

// arn is the ARN of topic: an ARN is used as is, a name is looked up in the listing.
func (t *topics) arn(ctx context.Context, topic string) (string, error) {
	if strings.HasPrefix(topic, "arn:") {
		return topic, nil
	}
	var token *string
	for {
		out, err := t.api.ListTopics(ctx, &sns.ListTopicsInput{NextToken: token})
		if err != nil {
			return "", mcpserve.AWSError(err)
		}
		for _, tp := range out.Topics {
			if topicName(aws.ToString(tp.TopicArn)) == topic {
				return aws.ToString(tp.TopicArn), nil
			}
		}
		if token = out.NextToken; token == nil {
			return "", fmt.Errorf("no topic named %s", topic)
		}
	}
}

type topicInput struct {
	Topic string `json:"topic" jsonschema:"topic ARN or name"`
}

type subscriptionSummary struct {
	ARN      string `json:"arn"`
	Protocol string `json:"protocol"`
	Endpoint string `json:"endpoint"`
	Pending  bool   `json:"pending,omitempty"`
}

type topicOutput struct {
	ARN               string                `json:"arn"`
	Name              string                `json:"name"`
	DisplayName       string                `json:"display_name,omitempty"`
	KMSKey            string                `json:"kms_key,omitempty"`
	Policy            string                `json:"policy,omitempty"`
	DeliveryPolicy    string                `json:"delivery_policy,omitempty"`
	SubscriptionsNext string                `json:"subscriptions_next,omitempty"`
	Attributes        map[string]string     `json:"attributes,omitempty"`
	Tags              map[string]string     `json:"tags,omitempty"`
	Subscriptions     []subscriptionSummary `json:"subscriptions"`
	FIFO              bool                  `json:"fifo"`
}

func (t *topics) Get(ctx context.Context, in *topicInput) (topicOutput, error) {
	arn, err := t.arn(ctx, in.Topic)
	if err != nil {
		return topicOutput{}, err
	}
	attrs, err := t.api.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(arn)})
	if err != nil {
		return topicOutput{}, mcpserve.AWSError(err)
	}
	subs, err := t.api.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{TopicArn: aws.String(arn)})
	if err != nil {
		return topicOutput{}, mcpserve.AWSError(err)
	}
	tags, err := t.api.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return topicOutput{}, mcpserve.AWSError(err)
	}
	a := attrs.Attributes
	res := topicOutput{
		ARN: arn, Name: topicName(arn), FIFO: a["FifoTopic"] == "true",
		DisplayName: a["DisplayName"], KMSKey: a["KmsMasterKeyId"], Policy: a["Policy"], DeliveryPolicy: a["DeliveryPolicy"],
		Attributes:        rest(a, "TopicArn", "DisplayName", "FifoTopic", "KmsMasterKeyId", "Policy", "DeliveryPolicy"),
		SubscriptionsNext: aws.ToString(subs.NextToken),
		Subscriptions:     make([]subscriptionSummary, 0, len(subs.Subscriptions)),
	}
	for _, s := range subs.Subscriptions {
		sa := aws.ToString(s.SubscriptionArn)
		res.Subscriptions = append(res.Subscriptions, subscriptionSummary{
			ARN: sa, Protocol: aws.ToString(s.Protocol), Endpoint: aws.ToString(s.Endpoint), Pending: sa == pendingARN,
		})
	}
	if len(tags.Tags) > 0 {
		res.Tags = make(map[string]string, len(tags.Tags))
		for _, tg := range tags.Tags {
			res.Tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
	}
	return res, nil
}

type subscriptionInput struct {
	ARN string `json:"arn" jsonschema:"subscription ARN, from get_topic"`
}

type subscriptionOutput struct {
	Attributes         map[string]string `json:"attributes,omitempty"`
	ARN                string            `json:"arn"`
	TopicARN           string            `json:"topic_arn"`
	Protocol           string            `json:"protocol"`
	Endpoint           string            `json:"endpoint"`
	FilterPolicy       string            `json:"filter_policy,omitempty"`
	FilterPolicyScope  string            `json:"filter_policy_scope,omitempty"`
	RedrivePolicy      string            `json:"redrive_policy,omitempty"`
	RawMessageDelivery bool              `json:"raw_message_delivery"`
	Pending            bool              `json:"pending,omitempty"`
}

func (t *topics) Subscription(ctx context.Context, in *subscriptionInput) (subscriptionOutput, error) {
	out, err := t.api.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(in.ARN)})
	if err != nil {
		return subscriptionOutput{}, mcpserve.AWSError(err)
	}
	a := out.Attributes
	return subscriptionOutput{
		ARN: in.ARN, TopicARN: a["TopicArn"], Protocol: a["Protocol"], Endpoint: a["Endpoint"],
		FilterPolicy: a["FilterPolicy"], FilterPolicyScope: a["FilterPolicyScope"], RedrivePolicy: a["RedrivePolicy"],
		RawMessageDelivery: a["RawMessageDelivery"] == "true", Pending: a["PendingConfirmation"] == "true",
		Attributes: rest(a, "SubscriptionArn", "TopicArn", "Protocol", "Endpoint", "FilterPolicy", "FilterPolicyScope",
			"RedrivePolicy", "RawMessageDelivery", "PendingConfirmation"),
	}, nil
}
