package sqsmcp

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
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

const fifoURL = "https://sqs.eu-west-3.amazonaws.com/1/orders.fifo"

// atLimitQueue peeks one at-limit message "a" so it is cached; mutate may adjust the message first.
func atLimitQueue(t *testing.T, url string, mutate func(*types.Message)) (*queues, *fakeSQS) {
	t.Helper()
	m := msg("a", strings.Repeat("x", maxBodyBytes+10), 5)
	if mutate != nil {
		mutate(&m)
	}
	f := &fakeSQS{queueURL: url, attrs: redriveAttrs(5), messages: []types.Message{m}}
	q := newQueues(f)
	if _, err := q.Peek(t.Context(), &peekInput{Queue: url}); err != nil {
		t.Fatal(err)
	}
	return q, f
}

func TestRepostRefusesAMessageNotRead(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL}
	_, err := newQueues(f).Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "zz"})
	if err == nil || !strings.Contains(err.Error(), "peek_messages it again") || f.calls["SendMessage"] != 0 {
		t.Fatalf("err = %v, sends = %d", err, f.calls["SendMessage"])
	}
}

func TestRepostRefusesAMessageNotAtLimit(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(5), messages: []types.Message{msg("a", "x", 1)}}
	q := newQueues(f)
	if _, err := q.Peek(t.Context(), &peekInput{Queue: "orders"}); err != nil {
		t.Fatal(err)
	}
	_, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "a"})
	if err == nil || !strings.Contains(err.Error(), "not at its receive limit") || f.calls["SendMessage"] != 0 {
		t.Fatalf("err = %v, sends = %d", err, f.calls["SendMessage"])
	}
}

func TestRepostSendsTheCachedCopyThenDeletes(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, func(m *types.Message) {
		m.MessageAttributes = map[string]types.MessageAttributeValue{"blob": {DataType: aws.String("Binary"), BinaryValue: []byte{1, 2, 3}}}
		m.Attributes["AWSTraceHeader"] = "Root=1-abc"
	})
	out, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	in := f.sendIn
	if aws.ToString(in.MessageBody) != strings.Repeat("x", maxBodyBytes+10) || string(in.MessageAttributes["blob"].BinaryValue) != "\x01\x02\x03" {
		t.Fatalf("send body/attributes wrong: len %d", len(aws.ToString(in.MessageBody)))
	}
	if tr := in.MessageSystemAttributes["AWSTraceHeader"]; aws.ToString(tr.StringValue) != "Root=1-abc" || aws.ToString(tr.DataType) != "String" {
		t.Fatalf("trace = %+v", tr)
	}
	if aws.ToString(f.deleteIn.ReceiptHandle) != "rh-a" || aws.ToString(f.deleteIn.QueueUrl) != peekURL {
		t.Fatalf("delete = %+v", f.deleteIn)
	}
	if !slices.Equal(f.order[len(f.order)-2:], []string{"SendMessage", "DeleteMessage"}) {
		t.Fatalf("order = %v", f.order)
	}
	if !out.DeleteAccepted || out.MessageID != "new-id" || out.Note == "" {
		t.Fatalf("out = %+v", out)
	}
}

func TestRepostDoesNotDeleteWhenTheSendFails(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, nil)
	f.sendErr = errors.New("boom")
	if _, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "a"}); err == nil {
		t.Fatal("no error")
	}
	if f.calls["DeleteMessage"] != 0 {
		t.Fatal("DeleteMessage called after a failed send")
	}
}

func TestRepostReportsAFailedDeleteWithoutFailing(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, nil)
	f.deleteErr = errors.New("expired")
	out, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "a"})
	if err != nil || out.DeleteAccepted || out.DeleteError != "expired" || out.Note == "" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestRepostOnFIFOUsesTheGroupAndANewDeduplicationID(t *testing.T) {
	q, f := atLimitQueue(t, fifoURL, func(m *types.Message) {
		m.Attributes["MessageGroupId"] = "g1"
		m.Attributes["MessageDeduplicationId"] = "orig"
	})
	if _, err := q.Repost(t.Context(), &repostInput{Queue: fifoURL, MessageID: "a"}); err != nil {
		t.Fatal(err)
	}
	in := f.sendIn
	if aws.ToString(in.MessageGroupId) != "g1" || len(aws.ToString(in.MessageDeduplicationId)) != 32 || aws.ToString(in.MessageDeduplicationId) == "orig" {
		t.Fatalf("group %q dedup %q", aws.ToString(in.MessageGroupId), aws.ToString(in.MessageDeduplicationId))
	}
}

func TestRepostAfterEvictionAsksToPeekAgain(t *testing.T) {
	f := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(5)}
	q := newQueues(f)
	for i := range 6 {
		f.messages = nil
		for j := range 10 {
			f.messages = append(f.messages, msg("m"+strconv.Itoa(i*10+j), "x", 5))
		}
		if _, err := q.Peek(t.Context(), &peekInput{Queue: "orders"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "m0"})
	if err == nil || !strings.Contains(err.Error(), "peek_messages it again") {
		t.Fatalf("err = %v", err)
	}
}

func TestRepostTwiceIsRefused(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, nil)
	if _, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := q.Repost(t.Context(), &repostInput{Queue: "orders", MessageID: "a"})
	if err == nil || !strings.Contains(err.Error(), "peek_messages it again") || f.calls["SendMessage"] != 1 {
		t.Fatalf("err = %v, sends = %d", err, f.calls["SendMessage"])
	}
}

func TestRepostConcurrentCallsSendOnce(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, nil)
	f.sendGate = make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := q.Repost(t.Context(), &repostInput{Queue: peekURL, MessageID: "a"})
			errs <- err
		}()
	}
	// The loser fails at once; the winner waits in SendMessage.
	first := <-errs
	close(f.sendGate)
	second := <-errs
	if (first == nil) == (second == nil) {
		t.Fatalf("errs = %v, %v: want exactly one failure", first, second)
	}
	loser := first
	if loser == nil {
		loser = second
	}
	if !strings.Contains(loser.Error(), "peek_messages it again") || f.calls["SendMessage"] != 1 {
		t.Fatalf("loser = %v, sends = %d", loser, f.calls["SendMessage"])
	}
}

func TestRepostFailedSendKeepsTheMessageAndWarnsTheCopyMayExist(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, nil)
	f.sendErr = errors.New("timeout")
	_, err := q.Repost(t.Context(), &repostInput{Queue: peekURL, MessageID: "a"})
	if err == nil || !strings.Contains(err.Error(), "may have been sent") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := q.cache.get(peekURL, "a"); !ok {
		t.Fatal("the message must stay cached after a failed send")
	}
	var o sqs.Options
	for _, fn := range f.sendOpts {
		fn(&o)
	}
	if o.RetryMaxAttempts != 1 {
		t.Fatalf("RetryMaxAttempts = %d, want 1", o.RetryMaxAttempts)
	}
}

func TestRepostKeepsTheGroupOnAStandardQueueWithoutDeduplicationID(t *testing.T) {
	q, f := atLimitQueue(t, peekURL, func(m *types.Message) { m.Attributes["MessageGroupId"] = "g1" })
	if _, err := q.Repost(t.Context(), &repostInput{Queue: peekURL, MessageID: "a"}); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.sendIn.MessageGroupId) != "g1" || f.sendIn.MessageDeduplicationId != nil {
		t.Fatalf("group %v dedup %v", f.sendIn.MessageGroupId, f.sendIn.MessageDeduplicationId)
	}
}

func TestPeekCutsLongAttributeValues(t *testing.T) {
	m := msg("a", "x", 1)
	m.MessageAttributes = map[string]types.MessageAttributeValue{"big": {StringValue: aws.String(strings.Repeat("y", maxBodyBytes+5))}}
	f := &fakeSQS{queueURL: peekURL, messages: []types.Message{m}}
	out, err := newQueues(f).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	got := out.Messages[0]
	if !got.Truncated || !strings.HasSuffix(got.Attributes["big"], truncatedSuffix) || len(got.Attributes["big"]) > maxBodyBytes+len(truncatedSuffix) {
		t.Fatalf("truncated %v, len %d", got.Truncated, len(got.Attributes["big"]))
	}
}

func TestPeekNoteMentionsTheDeadLetterQueueOnlyWithARedrivePolicy(t *testing.T) {
	const dlq = "moved to the dead-letter queue"
	with := &fakeSQS{queueURL: peekURL, attrs: redriveAttrs(5)}
	out, err := newQueues(with).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Note, dlq) {
		t.Fatalf("note = %q", out.Note)
	}
	without := &fakeSQS{queueURL: peekURL}
	out, err = newQueues(without).Peek(t.Context(), &peekInput{Queue: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Note, dlq) {
		t.Fatalf("note = %q", out.Note)
	}
}
