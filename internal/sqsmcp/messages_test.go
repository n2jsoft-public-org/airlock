package sqsmcp

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const peekURL = "https://sqs.eu-west-3.amazonaws.com/1/orders"

func redriveAttrs(n int) map[string]string {
	return map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sqs:eu-west-3:1:dlq","maxReceiveCount":` + strconv.Itoa(n) + `}`}
}

func msg(id, body string, count int) types.Message {
	return types.Message{
		MessageId: aws.String(id), Body: aws.String(body), ReceiptHandle: aws.String("rh-" + id),
		Attributes: map[string]string{"ApproximateReceiveCount": strconv.Itoa(count), "SentTimestamp": "1700000000000", "ApproximateFirstReceiveTimestamp": "1700000060000"},
	}
}

func TestPeekReceivesWithOneSecondVisibilityAndNeverDeletes(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(5), messages: []types.Message{msg("m1", "x", 1)}}
	if _, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders", Max: 3}); err != nil {
		t.Fatal(err)
	}
	in := f.recvIn
	if in.VisibilityTimeout != 1 || in.WaitTimeSeconds != 2 || in.MaxNumberOfMessages != 3 || len(in.MessageAttributeNames) != 1 || in.MessageAttributeNames[0] != "All" {
		t.Fatalf("input = %+v", in)
	}
	if f.calls["DeleteMessage"] != 0 {
		t.Fatal("DeleteMessage called")
	}
}

func TestPeekRefusesALowMaxReceiveCount(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(2)}
	_, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err == nil || !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "orders") {
		t.Fatalf("err = %v", err)
	}
	if f.calls["ReceiveMessage"] != 0 {
		t.Fatal("ReceiveMessage called")
	}
}

func TestPeekComputesDeliveriesLeftAndAtLimit(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(5), messages: []types.Message{msg("a", "x", 2), msg("b", "x", 4), msg("c", "x", 5)}}
	out, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	wantLeft, wantLimit := []int{3, 1, 0}, []bool{false, true, true}
	for i, m := range out.Messages {
		if m.DeliveriesLeft == nil || *m.DeliveriesLeft != wantLeft[i] || m.AtLimit != wantLimit[i] {
			t.Fatalf("message %d = %+v", i, m)
		}
	}
	if out.MaxReceiveCount != 5 || out.Messages[0].SentAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("out = %+v", out)
	}
}

func TestPeekWithoutRedriveHasNoLimit(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, messages: []types.Message{msg("a", "x", 9)}}
	out, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if m := out.Messages[0]; m.DeliveriesLeft != nil || m.AtLimit {
		t.Fatalf("message = %+v", m)
	}
}

func TestPeekCutsLongBodiesOnARuneBoundary(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, messages: []types.Message{msg("a", "x"+strings.Repeat("é", 20000), 1)}}
	out, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Messages[0]
	if !utf8.ValidString(m.Body) || !m.Truncated || !strings.HasSuffix(m.Body, truncatedSuffix) || len(m.Body) > maxBodyBytes+len(truncatedSuffix) {
		t.Fatalf("body len %d truncated %v", len(m.Body), m.Truncated)
	}
}

func TestPeekShowsBinaryAttributesAsTheirSize(t *testing.T) {
	m := msg("a", "x", 1)
	m.MessageAttributes = map[string]types.MessageAttributeValue{"blob": {BinaryValue: []byte{1, 2, 3}}, "s": {StringValue: aws.String("v")}}
	f := &fakeSQS{queueURL: peekURL, messages: []types.Message{m}}
	out, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if a := out.Messages[0].Attributes; a["blob"] != "<binary 3 bytes>" || a["s"] != "v" {
		t.Fatalf("attributes = %v", a)
	}
}

func TestPeekCachesTheMessages(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, messages: []types.Message{msg("a", "x", 1)}}
	q := newQueues(f)
	if _, err := q.Peek(t.Context(), &peekInput{Queue: "orders"}); err != nil {
		t.Fatal(err)
	}
	if m, ok := q.cache.get(peekURL, "a"); !ok || m.ReceiptHandle != "rh-a" {
		t.Fatalf("cached = %+v %v", m, ok)
	}
}

func TestPeekRecordsNoBody(t *testing.T) {
	m := msg("a", "SECRET-BODY", 1)
	m.MessageAttributes = map[string]types.MessageAttributeValue{"k": {StringValue: aws.String("SECRET-ATTR")}}
	f := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(5), messages: []types.Message{m}}
	cs, events := connect(t, f, true)
	if res := call(t, cs, "peek_messages", map[string]any{"queue": "orders"}); res.IsError {
		t.Fatalf("res = %+v", res)
	}
	e := (*events)[0]
	if e.Command != "peek_messages orders n=10" || e.Access != "read-write" || e.Rows == nil || *e.Rows != 1 {
		t.Fatalf("event = %+v", e)
	}
	raw, _ := json.Marshal(e) //nolint:errcheck // test.
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("event carries content: %s", raw)
	}
}
