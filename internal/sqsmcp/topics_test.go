package sqsmcp

import (
	"strings"
	"testing"

	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
)

const ordersARN = "arn:aws:sns:eu-west-3:1:orders-events"

func TestTopicsListNamesFromARN(t *testing.T) {
	f := &fakeSNS{topics: []string{ordersARN}, nextToken: "n2"}
	out, err := newTopics(f).List(t.Context(), &listTopicsInput{Start: "n1"})
	if err != nil || len(out.Topics) != 1 || out.Topics[0].Name != "orders-events" || out.Topics[0].ARN != ordersARN || out.Next != "n2" {
		t.Fatalf("out = %+v, %v", out, err)
	}
	if *f.list.NextToken != "n1" {
		t.Fatalf("input = %+v", f.list)
	}
}

func TestGetTopicResolvesANameAndListsSubscriptions(t *testing.T) {
	pending, confirmed := "PendingConfirmation", "arn:aws:sns:eu-west-3:1:orders-events:abc"
	proto, endpoint := "email", "a@b.c"
	f := &fakeSNS{
		topics: []string{"arn:aws:sns:eu-west-3:1:other", ordersARN},
		attrs:  map[string]string{"TopicArn": ordersARN, "DisplayName": "Orders", "FifoTopic": "true", "Policy": "{}", "SubscriptionsConfirmed": "1"},
		subs: []snstypes.Subscription{
			{SubscriptionArn: &pending, Protocol: &proto, Endpoint: &endpoint},
			{SubscriptionArn: &confirmed, Protocol: &proto, Endpoint: &endpoint},
		},
		tags: []snstypes.Tag{{Key: &proto, Value: &endpoint}},
	}
	out, err := newTopics(f).Get(t.Context(), &topicInput{Topic: "orders-events"})
	if err != nil {
		t.Fatal(err)
	}
	if *f.attrsIn.TopicArn != ordersARN || out.ARN != ordersARN || out.Name != "orders-events" || !out.FIFO || out.DisplayName != "Orders" || out.Policy != "{}" {
		t.Fatalf("out = %+v, in = %+v", out, f.attrsIn)
	}
	if out.Attributes["SubscriptionsConfirmed"] != "1" || len(out.Attributes) != 1 || out.Tags["email"] != "a@b.c" {
		t.Fatalf("attributes = %v, tags = %v", out.Attributes, out.Tags)
	}
	if len(out.Subscriptions) != 2 || !out.Subscriptions[0].Pending || out.Subscriptions[1].Pending || out.Subscriptions[1].Protocol != "email" {
		t.Fatalf("subscriptions = %+v", out.Subscriptions)
	}
}

func TestGetTopicUnknownNameFails(t *testing.T) {
	f := &fakeSNS{topics: []string{ordersARN}}
	_, err := newTopics(f).Get(t.Context(), &topicInput{Topic: "orders-missing"})
	if err == nil || !strings.Contains(err.Error(), "orders-missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetSubscriptionReportsFilterAndRawDelivery(t *testing.T) {
	f := &fakeSNS{subAttr: map[string]string{
		"TopicArn": ordersARN, "Protocol": "sqs", "Endpoint": "arn:aws:sqs:eu-west-3:1:q",
		"FilterPolicy": `{"type":["paid"]}`, "RawMessageDelivery": "true", "PendingConfirmation": "false", "Owner": "1",
	}}
	arn := ordersARN + ":abc"
	out, err := newTopics(f).Subscription(t.Context(), &subscriptionInput{ARN: arn})
	if err != nil {
		t.Fatal(err)
	}
	if *f.subAttrs.SubscriptionArn != arn || out.FilterPolicy != `{"type":["paid"]}` || !out.RawMessageDelivery || out.Pending ||
		out.TopicARN != ordersARN || out.Protocol != "sqs" || out.Attributes["Owner"] != "1" || len(out.Attributes) != 1 {
		t.Fatalf("out = %+v", out)
	}
}
