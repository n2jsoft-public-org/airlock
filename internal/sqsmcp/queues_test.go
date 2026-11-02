package sqsmcp

import (
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestParseRedriveAcceptsNumberAndString(t *testing.T) {
	for _, policy := range []string{
		`{"deadLetterTargetArn":"arn:dlq","maxReceiveCount":5}`,
		`{"deadLetterTargetArn":"arn:dlq","maxReceiveCount":"5"}`,
	} {
		r, err := parseRedrive(policy)
		if err != nil || r == nil || r.DeadLetterTargetARN != "arn:dlq" || r.MaxReceiveCount != 5 {
			t.Fatalf("parseRedrive(%s) = %+v, %v", policy, r, err)
		}
	}
	if r, err := parseRedrive(""); r != nil || err != nil {
		t.Fatalf("empty = %+v, %v", r, err)
	}
	if _, err := parseRedrive(`{"maxReceiveCount":"x"}`); err == nil {
		t.Fatal("want an error")
	}
}

func TestURLUsesAURLAsIsAndResolvesAName(t *testing.T) {
	f := &fakeSQS{queueURL: "https://sqs.eu-west-3.amazonaws.com/1/orders.fifo"}
	q := newQueues(f)
	got, err := q.url(t.Context(), "https://sqs.eu-west-3.amazonaws.com/1/orders")
	if err != nil || got != "https://sqs.eu-west-3.amazonaws.com/1/orders" || f.calls["GetQueueUrl"] != 0 {
		t.Fatalf("url = %q, %v, calls %v", got, err, f.calls)
	}
	got, err = q.url(t.Context(), "orders.fifo")
	if err != nil || got != f.queueURL || *f.urlIn.QueueName != "orders.fifo" {
		t.Fatalf("url = %q, %v", got, err)
	}
}

func TestListPassesPrefixAndPageAndReportsFIFO(t *testing.T) {
	f := &fakeSQS{
		urls:      []string{"https://sqs.eu-west-3.amazonaws.com/1/orders", "https://sqs.eu-west-3.amazonaws.com/1/orders.fifo"},
		nextToken: "n",
	}
	out, err := newQueues(f).List(t.Context(), &listQueuesInput{Prefix: "ord", Start: "t"})
	if err != nil {
		t.Fatal(err)
	}
	in := f.list
	if *in.QueueNamePrefix != "ord" || *in.MaxResults != 100 || *in.NextToken != "t" {
		t.Fatalf("input = %+v", in)
	}
	if len(out.Queues) != 2 || out.Queues[0].Name != "orders" || out.Queues[0].FIFO || !out.Queues[1].FIFO || out.Next != "n" {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetParsesAttributesRedriveTagsAndSources(t *testing.T) {
	f := &fakeSQS{
		queueURL: "https://sqs.eu-west-3.amazonaws.com/1/orders",
		attrs: map[string]string{
			"QueueArn":                              "arn:aws:sqs:eu-west-3:1:orders",
			"ApproximateNumberOfMessages":           "12",
			"ApproximateNumberOfMessagesNotVisible": "3",
			"ApproximateNumberOfMessagesDelayed":    "1",
			"VisibilityTimeout":                     "30",
			"RedrivePolicy":                         `{"deadLetterTargetArn":"arn:dlq","maxReceiveCount":5}`,
		},
		tags:    map[string]string{"team": "pay"},
		sources: []string{"https://sqs.eu-west-3.amazonaws.com/1/orders"},
	}
	out, err := newQueues(f).Get(t.Context(), &queueInput{Queue: "https://sqs.eu-west-3.amazonaws.com/1/orders"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "orders" || out.ARN != "arn:aws:sqs:eu-west-3:1:orders" || out.Visible != 12 || out.InFlight != 3 || out.Delayed != 1 ||
		out.VisibilityTimeout != 30 || out.RedrivePolicy == nil || out.RedrivePolicy.MaxReceiveCount != 5 ||
		out.Tags["team"] != "pay" || !slices.Equal(out.SourceQueues, f.sources) {
		t.Fatalf("out = %+v", out)
	}
	if !slices.Equal(f.attrsIn.AttributeNames, []types.QueueAttributeName{types.QueueAttributeNameAll}) {
		t.Fatalf("names = %v", f.attrsIn.AttributeNames)
	}
}
